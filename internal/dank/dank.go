package dank

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// DankEncoder implementation matching Python's C++ backend exactly
// Uses Brzozowski's algorithm for DFA minimization

// preprocessRegex expands character classes like [1-5] to (1|2|3|4|5)
func preprocessRegex(regex string) string {
	// Find all character classes of form [x-y]
	re := regexp.MustCompile(`\[(.)\-(.)\]`)

	for {
		match := re.FindStringSubmatchIndex(regex)
		if match == nil {
			break
		}

		// Extract start and end characters
		startChar := regex[match[2]]
		endChar := regex[match[4]]

		// Expand range
		var elements []string
		for ch := startChar; ch <= endChar; ch++ {
			elements = append(elements, string(ch))
		}
		expanded := "(" + strings.Join(elements, "|") + ")"

		// Replace in regex
		fullMatch := regex[match[0]:match[1]]
		regex = strings.Replace(regex, fullMatch, expanded, 1)
	}

	return regex
}

type transition struct {
	to    int
	label byte
}

type nfaState struct {
	edges     []transition
	accepting bool
}

type nfa struct {
	states []nfaState
	starts []int
}

type dfaState struct {
	edges     []transition // explicit transitions, in byte order
	accepting bool
}

// State 0 is the start. Missing transitions go to an implicit dead state.
type dfa []dfaState

// DankEncoder generates and counts words with a minimized DFA.
type DankEncoder struct {
	dfa          dfa
	numNFAStates int
}

// NewDankEncoder compiles a regex into a minimized DFA using Brzozowski's algorithm.
func NewDankEncoder(regexStr string) *DankEncoder {
	forward := buildNFAFromRegex(regexStr).determinize()
	reversed := forward.reverse().determinize().reverse()
	return &DankEncoder{
		dfa:          reversed.determinize(),
		numNFAStates: len(reversed.states) + 1, // Historically included the dead state.
	}
}

// CountWords counts accepted words in the inclusive length range without minimizing
// the DFA. It returns math.MaxInt64 if the count does not fit in an int64.
func CountWords(regexStr string, minLen, maxLen int) int64 {
	return buildNFAFromRegex(regexStr).determinize().numWords(minLen, maxLen)
}

func buildNFAFromRegex(regex string) nfa {
	n := nfa{
		states: []nfaState{{}, {accepting: true}},
		starts: []int{0},
	}
	n.fromRegex(0, 1, preprocessRegex(regex))
	return n
}

// fromRegex implements _from_regex from C++
func (d *nfa) fromRegex(s, t int, pattern string) {
	if len(pattern) == 0 {
		d.insertNFA(s, 0, t) // epsilon transition
		return
	}

	// Single character (including escaped)
	if len(pattern) == 1 {
		d.insertNFA(s, pattern[0], t)
		return
	}

	// Escaped character
	if len(pattern) == 2 && pattern[0] == '\\' {
		d.insertNFA(s, pattern[1], t)
		return
	}

	// Find rightmost top-level | or concatenation point
	optionPos := -1
	concatPos := -1
	depth := 0

	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]

		switch ch {
		case '\\':
			if depth == 0 {
				concatPos = i
			}
			i++ // Skip next char
		case '(':
			if depth == 0 {
				concatPos = i
			}
			depth++
		case ')':
			depth--
		case '|':
			if depth == 0 {
				optionPos = i
			}
		case '?', '*', '+':
			// Don't update concat for operators
		default:
			if depth == 0 {
				concatPos = i
			}
		}
	}

	// Handle alternation (|)
	if optionPos >= 0 {
		// Create intermediate states
		i0 := d.newState()
		i1 := d.newState()
		d.insertNFA(s, 0, i0) // epsilon
		d.insertNFA(i1, 0, t) // epsilon
		d.fromRegex(i0, i1, pattern[:optionPos])

		i0 = d.newState()
		i1 = d.newState()
		d.insertNFA(s, 0, i0) // epsilon
		d.insertNFA(i1, 0, t) // epsilon
		d.fromRegex(i0, i1, pattern[optionPos+1:])
		return
	}

	// Handle concatenation
	if concatPos > 0 {
		i0 := d.newState()
		i1 := d.newState()
		d.insertNFA(i0, 0, i1) // epsilon
		d.fromRegex(s, i0, pattern[:concatPos])
		d.fromRegex(i1, t, pattern[concatPos:])
		return
	}

	// Handle postfix operators
	lastChar := pattern[len(pattern)-1]

	if lastChar == '?' {
		i0 := d.newState()
		i1 := d.newState()
		d.insertNFA(s, 0, i0) // epsilon
		d.insertNFA(s, 0, t)  // epsilon (skip)
		d.insertNFA(i1, 0, t) // epsilon
		d.fromRegex(i0, i1, pattern[:len(pattern)-1])
		return
	}

	if lastChar == '*' {
		i0 := d.newState()
		i1 := d.newState()
		d.insertNFA(s, 0, i0)  // epsilon
		d.insertNFA(s, 0, t)   // epsilon (skip)
		d.insertNFA(i1, 0, i0) // epsilon (loop)
		d.insertNFA(i1, 0, t)  // epsilon (exit)
		d.fromRegex(i0, i1, pattern[:len(pattern)-1])
		return
	}

	if lastChar == '+' {
		i0 := d.newState()
		i1 := d.newState()
		d.insertNFA(i0, 0, i1) // epsilon
		d.fromRegex(s, i0, pattern[:len(pattern)-1])

		s = i1
		i0 = d.newState()
		i1 = d.newState()
		d.insertNFA(s, 0, i0)  // epsilon
		d.insertNFA(s, 0, t)   // epsilon (skip)
		d.insertNFA(i1, 0, i0) // epsilon (loop)
		d.insertNFA(i1, 0, t)  // epsilon (exit)
		d.fromRegex(i0, i1, pattern[:len(pattern)-1])
		return
	}

	// Must be wrapped in parentheses
	if pattern[0] == '(' && pattern[len(pattern)-1] == ')' {
		d.fromRegex(s, t, pattern[1:len(pattern)-1])
		return
	}

	// Shouldn't reach here
	panic(fmt.Sprintf("Unexpected pattern: %s", pattern))
}

func (n *nfa) newState() int {
	id := len(n.states)
	n.states = append(n.states, nfaState{})
	return id
}

func (n *nfa) insertNFA(from int, ch byte, to int) {
	n.states[from].edges = append(n.states[from].edges, transition{to: to, label: ch})
}

// determinize builds only reachable subsets. The scratch slices and membership
// marks are reused across rows, and only new subsets keep a copy of their IDs.
func (n nfa) determinize() dfa {
	var rows dfa
	var subsets [][]int
	idsBySet := make(map[string]int)
	seen := make([]bool, len(n.states))
	var closed []int
	var key []byte
	intern := func(seeds []int) int {
		closed = closed[:0]
		for _, id := range seeds {
			if !seen[id] {
				seen[id] = true
				closed = append(closed, id)
			}
		}
		for i := 0; i < len(closed); i++ {
			for _, edge := range n.states[closed[i]].edges {
				if edge.label == 0 && !seen[edge.to] {
					seen[edge.to] = true
					closed = append(closed, edge.to)
				}
			}
		}
		slices.Sort(closed)
		key = key[:0]
		accepting := false
		for _, id := range closed {
			seen[id] = false
			key = binary.AppendUvarint(key, uint64(id))
			accepting = accepting || n.states[id].accepting
		}
		if id, ok := idsBySet[string(key)]; ok {
			return id
		}
		id := len(rows)
		idsBySet[string(key)] = id
		rows = append(rows, dfaState{accepting: accepting})
		subsets = append(subsets, slices.Clone(closed))
		return id
	}

	intern(n.starts)
	var moves [256][]int
	var labels []byte
	for state := 0; state < len(subsets); state++ {
		labels = labels[:0]
		for _, id := range subsets[state] {
			for _, edge := range n.states[id].edges {
				if edge.label == 0 {
					continue
				}
				if len(moves[edge.label]) == 0 {
					labels = append(labels, edge.label)
				}
				moves[edge.label] = append(moves[edge.label], edge.to)
			}
		}
		subsets[state] = nil

		slices.Sort(labels)
		if len(labels) > 0 {
			edges := make([]transition, 0, len(labels))
			for _, label := range labels {
				edges = append(edges, transition{to: intern(moves[label]), label: label})
				moves[label] = moves[label][:0]
			}
			rows[state].edges = edges
		}
	}
	return rows
}

// reverse omits the implicit dead state: it cannot reach a final state.
func (d dfa) reverse() nfa {
	reversed := nfa{states: make([]nfaState, len(d))}
	reversed.states[0].accepting = true
	for from, row := range d {
		if row.accepting {
			reversed.starts = append(reversed.starts, from)
		}
		for _, edge := range row.edges {
			reversed.insertNFA(edge.to, edge.label, from)
		}
	}
	return reversed
}

// NumWords counts accepted words in the inclusive length range on the compiled
// DFA. It returns math.MaxInt64 if the count does not fit in an int64.
func (d *DankEncoder) NumWords(minLen, maxLen int) int64 {
	return d.dfa.numWords(minLen, maxLen)
}

func (d dfa) numWords(minLen, maxLen int) int64 {
	if maxLen < 0 || minLen > maxLen || len(d) == 0 {
		return 0
	}
	current := make([]int64, len(d))
	next := make([]int64, len(d))
	current[0] = 1
	var total int64
	for length := 0; ; length++ {
		last := length == maxLen
		inRange := length >= minLen
		// Saturated paths may still be outside the requested lengths or end
		// in a non-accepting state. Only the accepted total can stop the count.
		hasNext := false
		for state, ways := range current {
			if ways == 0 {
				continue
			}
			if inRange && d[state].accepting {
				total = saturatingAdd(total, ways)
				if total == math.MaxInt64 {
					return total
				}
			}
			if !last && len(d[state].edges) > 0 {
				hasNext = true
				for _, edge := range d[state].edges {
					next[edge.to] = saturatingAdd(next[edge.to], ways)
				}
			}
		}
		if !hasNext {
			return total
		}
		current, next = next, current
		clear(next)
	}
}

func saturatingAdd(x, y int64) int64 {
	if y > math.MaxInt64-x {
		return math.MaxInt64
	}
	return x + y
}

// GenerateAtFixedLength returns all strings of exactly fixedLen
func (d *DankEncoder) GenerateAtFixedLength(fixedLen int) []string {
	results, _ := d.GenerateAtFixedLengthWithContextAndLimit(context.Background(), fixedLen, 0)
	return results
}

// GenerateAtFixedLengthWithContext returns all strings of exactly fixedLen with context cancellation support
func (d *DankEncoder) GenerateAtFixedLengthWithContext(ctx context.Context, fixedLen int) ([]string, error) {
	return d.GenerateAtFixedLengthWithContextAndLimit(ctx, fixedLen, 0)
}

// GenerateAtFixedLengthWithLimit returns up to maxResults strings of exactly fixedLen
func (d *DankEncoder) GenerateAtFixedLengthWithLimit(fixedLen int, maxResults int) []string {
	results, _ := d.GenerateAtFixedLengthWithContextAndLimit(context.Background(), fixedLen, maxResults)
	return results
}

// GenerateAtFixedLengthWithContextAndLimit returns up to maxResults strings of exactly fixedLen with context cancellation support
func (d *DankEncoder) GenerateAtFixedLengthWithContextAndLimit(ctx context.Context, fixedLen int, maxResults int) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fixedLen < 0 {
		return nil, nil
	}
	if maxResults < 0 {
		maxResults = 0
	}
	var results []string
	if maxResults > 0 {
		capacity := maxResults
		if capacity > 1024 {
			capacity = 1024
		}
		results = make([]string, 0, capacity)
	}
	live, err := d.dfa.liveStates(ctx, fixedLen)
	if err != nil {
		return results, err
	}
	if fixedLen >= len(live) || len(d.dfa) == 0 || !live[fixedLen][0] {
		return results, ctx.Err()
	}
	word := make([]byte, 0, fixedLen)
	// The rows are already ordered by byte, so DFS yields the sorted prefix.
	err = d.dfa.generateFixed(ctx, 0, word, fixedLen, maxResults, live, &results)
	return results, err
}

func (d dfa) generateFixed(ctx context.Context, state int, word []byte, remaining, maxResults int, live [][]bool, results *[]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if remaining == 0 {
		// Only states that can finish in exactly the remaining length are visited.
		*results = append(*results, string(word))
		return nil
	}
	for _, edge := range d[state].edges {
		if maxResults > 0 && len(*results) >= maxResults {
			break
		}
		if live[remaining-1][edge.to] {
			// As with string(label), each pattern byte becomes a rune, even outside ASCII.
			next := utf8.AppendRune(word, rune(edge.label))
			if err := d.generateFixed(ctx, edge.to, next, remaining-1, maxResults, live, results); err != nil {
				return err
			}
		}
	}
	return nil
}

// liveStates reports which states can reach a final state in exactly r steps.
// Once a row is empty, every longer length is impossible as well.
func (d dfa) liveStates(ctx context.Context, length int) ([][]bool, error) {
	first := make([]bool, len(d))
	anyLive := false
	for state, row := range d {
		first[state] = row.accepting
		anyLive = anyLive || row.accepting
	}
	live := [][]bool{first}
	done := ctx.Done()
	for len(live) <= length && anyLive {
		previous := live[len(live)-1]
		current := make([]bool, len(d))
		anyLive = false
		for state, row := range d {
			if state%256 == 0 {
				select {
				case <-done:
					return nil, ctx.Err()
				default:
				}
			}
			for _, edge := range row.edges {
				if previous[edge.to] {
					current[state] = true
					anyLive = true
					break
				}
			}
		}
		live = append(live, current)
	}
	return live, nil
}

// NumStates returns the number of DFA states
func (d *DankEncoder) NumStates() int {
	if len(d.dfa) == 0 {
		return 0
	}
	return len(d.dfa) + 1 // Include the implicit dead state.
}

// NumNFAStates returns the number of NFA states (for debugging)
func (d *DankEncoder) NumNFAStates() int {
	return d.numNFAStates
}
