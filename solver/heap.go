package solver

// heap is a binary max-heap of variables keyed by activity, with ties broken
// towards the lower variable number so the search is deterministic. It
// replaces a linear scan over every variable at every decision, which was
// fine for one solve and quadratic for thousands of them.
type heap struct {
	act  *[]float64
	data []int
	pos  []int // variable -> index in data, -1 when absent
}

func (h *heap) ensure(v int) {
	for len(h.pos) <= v {
		h.pos = append(h.pos, -1)
	}
}

func (h *heap) len() int            { return len(h.data) }
func (h *heap) contains(v int) bool { return v < len(h.pos) && h.pos[v] >= 0 }

func (h *heap) less(a, b int) bool {
	act := *h.act
	if act[a] != act[b] {
		return act[a] > act[b]
	}
	return a < b
}

func (h *heap) push(v int) {
	h.ensure(v)
	h.pos[v] = len(h.data)
	h.data = append(h.data, v)
	h.up(v)
}

func (h *heap) pop() int {
	top := h.data[0]
	last := h.data[len(h.data)-1]
	h.data = h.data[:len(h.data)-1]
	h.pos[top] = -1
	if len(h.data) > 0 {
		h.data[0] = last
		h.pos[last] = 0
		h.down(0)
	}
	return top
}

func (h *heap) up(v int) {
	i := h.pos[v]
	for i > 0 {
		p := (i - 1) / 2
		if !h.less(v, h.data[p]) {
			break
		}
		h.data[i] = h.data[p]
		h.pos[h.data[i]] = i
		i = p
	}
	h.data[i] = v
	h.pos[v] = i
}

func (h *heap) down(i int) {
	v := h.data[i]
	n := len(h.data)
	for {
		c := 2*i + 1
		if c >= n {
			break
		}
		if c+1 < n && h.less(h.data[c+1], h.data[c]) {
			c++
		}
		if !h.less(h.data[c], v) {
			break
		}
		h.data[i] = h.data[c]
		h.pos[h.data[i]] = i
		i = c
	}
	h.data[i] = v
	h.pos[v] = i
}
