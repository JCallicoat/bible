package bible

import (
	"sort"
)

// CanonicalBooks is the canonical book order/names. It's only used for
// "did you mean" suggestions when a book can't be resolved; resolution
// itself always runs against a translation's actual books.
var CanonicalBooks = []string{
	"Genesis", "Exodus", "Leviticus", "Numbers", "Deuteronomy", "Joshua",
	"Judges", "Ruth", "1 Samuel", "2 Samuel", "1 Kings", "2 Kings",
	"1 Chronicles", "2 Chronicles", "Ezra", "Nehemiah", "Esther", "Job",
	"Psalm", "Proverbs", "Ecclesiastes", "Song Of Solomon", "Isaiah",
	"Jeremiah", "Lamentations", "Ezekiel", "Daniel", "Hosea", "Joel", "Amos",
	"Obadiah", "Jonah", "Micah", "Nahum", "Habakkuk", "Zephaniah", "Haggai",
	"Zechariah", "Malachi", "Tobit", "Judith", "Baruch", "Sirach",
	"1 Maccabees", "2 Maccabees", "Wisdom", "Prayer of Manasseh", "1 Esdras",
	"2 Esdras", "3 Maccabees", "4 Maccabees", "Matthew", "Mark", "Luke",
	"John", "Acts", "Romans", "1 Corinthians", "2 Corinthians", "Galatians",
	"Ephesians", "Philippians", "Colossians", "1 Thessalonians",
	"2 Thessalonians", "1 Timothy", "2 Timothy", "Titus", "Philemon",
	"Hebrews", "James", "1 Peter", "2 Peter", "1 John", "2 John", "3 John",
	"Jude", "Revelation",
}

// SuggestBooks returns up to three canonical book names close to input,
// best first: those with a similarity ratio of at least 0.6 to input.
func SuggestBooks(input string) []string {
	return closeMatches(titleCase(input), CanonicalBooks, 3, 0.6)
}

func closeMatches(word string, possibilities []string, n int, cutoff float64) []string {
	type scored struct {
		score float64
		s     string
	}
	b := []rune(word)
	m := newMatcher(b)
	var res []scored
	for _, p := range possibilities {
		if r := m.ratio([]rune(p)); r >= cutoff {
			res = append(res, scored{r, p})
		}
	}
	// heapq.nlargest on (score, string) tuples: ties go to the larger string.
	sort.Slice(res, func(i, j int) bool {
		if res[i].score != res[j].score {
			return res[i].score > res[j].score
		}
		return res[i].s > res[j].s
	})
	var out []string
	for i := 0; i < len(res) && i < n; i++ {
		out = append(out, res[i].s)
	}
	return out
}

// matcher is a port of the parts of difflib.SequenceMatcher (isjunk=None,
// autojunk=True) needed for ratio(), with b as the fixed second sequence.
type matcher struct {
	b       []rune
	b2j     map[rune][]int
	popular map[rune]bool
}

func newMatcher(b []rune) *matcher {
	m := &matcher{b: b, b2j: map[rune][]int{}, popular: map[rune]bool{}}
	for i, r := range b {
		m.b2j[r] = append(m.b2j[r], i)
	}
	if n := len(b); n >= 200 {
		ntest := n/100 + 1
		for r, idx := range m.b2j {
			if len(idx) > ntest {
				m.popular[r] = true
			}
		}
		for r := range m.popular {
			delete(m.b2j, r)
		}
	}
	return m
}

func (m *matcher) ratio(a []rune) float64 {
	total := len(a) + len(m.b)
	if total == 0 {
		return 1.0
	}
	matches := 0
	type span struct{ alo, ahi, blo, bhi int }
	queue := []span{{0, len(a), 0, len(m.b)}}
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		i, j, k := m.longestMatch(a, q.alo, q.ahi, q.blo, q.bhi)
		if k == 0 {
			continue
		}
		matches += k
		if q.alo < i && q.blo < j {
			queue = append(queue, span{q.alo, i, q.blo, j})
		}
		if i+k < q.ahi && j+k < q.bhi {
			queue = append(queue, span{i + k, q.ahi, j + k, q.bhi})
		}
	}
	return 2.0 * float64(matches) / float64(total)
}

// longestMatch ports SequenceMatcher.find_longest_match. With isjunk=None
// the junk set is empty, so only the non-junk extension loops can apply.
func (m *matcher) longestMatch(a []rune, alo, ahi, blo, bhi int) (int, int, int) {
	b := m.b
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		newj2len := map[int]int{}
		for _, j := range m.b2j[a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	for besti > alo && bestj > blo && a[besti-1] == b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && a[besti+bestsize] == b[bestj+bestsize] {
		bestsize++
	}
	return besti, bestj, bestsize
}
