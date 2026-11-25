package zlgz

import (
	"cmp"
	"slices"
)

const Empty int8 = -1

// Set at build time for clients where horizontal pushes are more reliable.
var shiftPolicy string

const maxCells = 160

// compactBoard 让搜索状态按值保存。子状态只需在栈上复制一个小数组，
// DFS 不再为每条搜索边分配一个 []int8。
type compactBoard [maxCells]int8

// moveEnumWorkspace 缓存每个格子四个方向上最近的非空砖块。它让候选枚举
// 不必针对每个移动距离反复穿过同一段空格。14x10 棋盘仅占约 1.9 KiB，
// guided 搜索为每个 worker 独享一份，避免锁和临时分配。
type moveEnumWorkspace struct {
	nearest [maxCells][4]int16
	shift   [maxCells][4]uint8
}

func (workspace *moveEnumWorkspace) build(b *compactBoard, rows, cols int) {
	for col := 0; col < cols; col++ {
		nearest := int16(-1)
		emptyRun := 0
		for row := 0; row < rows; row++ {
			index := row*cols + col
			workspace.nearest[index][0] = nearest
			if b[index] == Empty {
				emptyRun++
				continue
			}
			if row > 0 && b[index-cols] != Empty {
				workspace.shift[index][0] = workspace.shift[index-cols][0]
			} else {
				workspace.shift[index][0] = uint8(emptyRun)
			}
			emptyRun = 0
			nearest = int16(index)
		}
		nearest = -1
		emptyRun = 0
		for row := rows - 1; row >= 0; row-- {
			index := row*cols + col
			workspace.nearest[index][1] = nearest
			if b[index] == Empty {
				emptyRun++
				continue
			}
			if row+1 < rows && b[index+cols] != Empty {
				workspace.shift[index][1] = workspace.shift[index+cols][1]
			} else {
				workspace.shift[index][1] = uint8(emptyRun)
			}
			emptyRun = 0
			nearest = int16(index)
		}
	}
	for row := 0; row < rows; row++ {
		nearest := int16(-1)
		emptyRun := 0
		for col := 0; col < cols; col++ {
			index := row*cols + col
			workspace.nearest[index][2] = nearest
			if b[index] == Empty {
				emptyRun++
				continue
			}
			if col > 0 && b[index-1] != Empty {
				workspace.shift[index][2] = workspace.shift[index-1][2]
			} else {
				workspace.shift[index][2] = uint8(emptyRun)
			}
			emptyRun = 0
			nearest = int16(index)
		}
		nearest = -1
		emptyRun = 0
		for col := cols - 1; col >= 0; col-- {
			index := row*cols + col
			workspace.nearest[index][3] = nearest
			if b[index] == Empty {
				emptyRun++
				continue
			}
			if col+1 < cols && b[index+1] != Empty {
				workspace.shift[index][3] = workspace.shift[index+1][3]
			} else {
				workspace.shift[index][3] = uint8(emptyRun)
			}
			emptyRun = 0
			nearest = int16(index)
		}
	}
}

// searchMove 是热路径使用的紧凑表示。公开 JSON API 的 Move 使用 int 字段，
// 在 64 位系统上明显更大。
type searchMove struct {
	tileID                 int8
	sr, sc, tr, tc, lr, lc uint8
	dr, dc                 int8
	shift                  uint8
	rank                   uint32
}

func (m searchMove) public() Move {
	return Move{
		TileID: int(m.tileID),
		SR:     int(m.sr), SC: int(m.sc),
		TR: int(m.tr), TC: int(m.tc),
		LR: int(m.lr), LC: int(m.lc),
		DR: int(m.dr), DC: int(m.dc),
		Shift: int(m.shift),
	}
}

var dirs = [4][2]int{
	{-1, 0}, // 上
	{1, 0},  // 下
	{0, -1}, // 左
	{0, 1},  // 右
}

type Move struct {
	TileID int
	SR, SC int
	TR, TC int
	LR, LC int
	DR, DC int
	Shift  int
}

func at(b []int8, cols, r, c int) int8 {
	return b[r*cols+c]
}

func set(b []int8, cols, r, c int, v int8) {
	b[r*cols+c] = v
}

func inside(r, c, rows, cols int) bool {
	return r >= 0 && r < rows && c >= 0 && c < cols
}

func remaining(b []int8, n int) int {
	count := 0
	for i := 0; i < n; i++ {
		if b[i] != Empty {
			count++
		}
	}
	return count
}

func oddTypes(b []int8, n int) bool {
	var counts [256]uint8
	for i := 0; i < n; i++ {
		v := b[i]
		if v == Empty {
			continue
		}
		counts[uint8(v)]++
	}
	for _, count := range counts {
		if count%2 != 0 {
			return true
		}
	}
	return false
}

func findEnd(b []int8, rows, cols, row, col, dr, dc int) (int, int) {
	cr, cc := row, col
	for {
		nr, nc := cr+dr, cc+dc
		if !inside(nr, nc, rows, cols) || at(b, cols, nr, nc) == Empty {
			return cr, cc
		}
		cr, cc = nr, nc
	}
}

func shiftCapacity(b []int8, rows, cols, row, col, dr, dc int) int {
	cr, cc := findEnd(b, rows, cols, row, col, dr, dc)
	dist := 0
	for {
		nr, nc := cr+dr, cc+dc
		if !inside(nr, nc, rows, cols) || at(b, cols, nr, nc) != Empty {
			return dist
		}
		dist++
		cr, cc = nr, nc
	}
}

func findSame(b []int8, rows, cols, sr, sc, candR, candC int, tile int8) (int, int, bool) {
	for _, d := range dirs {
		row, col := candR+d[0], candC+d[1]
		for inside(row, col, rows, cols) {
			if row == sr && col == sc {
				break
			}
			v := at(b, cols, row, col)
			if v == Empty {
				row += d[0]
				col += d[1]
				continue
			}
			if v == tile {
				return row, col, true
			}
			break
		}
	}
	return 0, 0, false
}

func findSameIndexed(
	b *compactBoard,
	cols, sr, sc, candR, candC int,
	tile int8,
	workspace *moveEnumWorkspace,
) (int, int, bool) {
	source := sr*cols + sc
	candidate := candR*cols + candC
	for direction := range dirs {
		found := workspace.nearest[candidate][direction]
		if found < 0 || int(found) == source {
			continue
		}
		index := int(found)
		if b[index] == tile {
			return index / cols, index % cols, true
		}
	}
	return 0, 0, false
}

func onRay(sr, sc, tr, tc, dr, dc int) bool {
	if dr != 0 {
		return sc == tc && (tr-sr)*dr > 0
	}
	return sr == tr && (tc-sc)*dc > 0
}

func blockedByRay(b []int8, rows, cols, sr, sc, tr, tc, dr, dc int) bool {
	if !onRay(sr, sc, tr, tc, dr, dc) {
		return false
	}
	row, col := sr+dr, sc+dc
	for inside(row, col, rows, cols) {
		if row == tr && col == tc {
			return false
		}
		if at(b, cols, row, col) != Empty {
			return true
		}
		row += dr
		col += dc
	}
	return false
}

func blockedByRayIndexed(
	cols, sr, sc, tr, tc, dr, dc, direction int,
	workspace *moveEnumWorkspace,
) bool {
	if !onRay(sr, sc, tr, tc, dr, dc) {
		return false
	}
	return int(workspace.nearest[sr*cols+sc][direction]) != tr*cols+tc
}

func applyMove(b []int8, rows, cols int, m Move) ([]int8, bool) {
	n := len(b)
	out := make([]int8, n)
	copy(out, b)
	set(out, cols, m.SR, m.SC, Empty)
	set(out, cols, m.TR, m.TC, Empty)
	if m.Shift <= 0 {
		return out, true
	}
	er, ec := findEnd(out, rows, cols, m.SR, m.SC, m.DR, m.DC)
	type cell struct{ r, c int }
	cells := make([]cell, 0, 16)
	cr, cc := er, ec
	for cr != m.SR || cc != m.SC {
		cells = append(cells, cell{cr, cc})
		cr -= m.DR
		cc -= m.DC
	}
	for _, c := range cells {
		nr := c.r + m.DR*m.Shift
		nc := c.c + m.DC*m.Shift
		if !inside(nr, nc, rows, cols) {
			return nil, false
		}
		set(out, cols, nr, nc, at(out, cols, c.r, c.c))
		set(out, cols, c.r, c.c, Empty)
	}
	return out, true
}

func compareSearchMove(a, b searchMove) int {
	if d := cmp.Compare(a.rank, b.rank); d != 0 {
		return d
	}
	if d := cmp.Compare(a.sr, b.sr); d != 0 {
		return d
	}
	if d := cmp.Compare(a.sc, b.sc); d != 0 {
		return d
	}
	if d := cmp.Compare(a.tr, b.tr); d != 0 {
		return d
	}
	if d := cmp.Compare(a.tc, b.tc); d != 0 {
		return d
	}
	if d := cmp.Compare(a.lr, b.lr); d != 0 {
		return d
	}
	if d := cmp.Compare(a.lc, b.lc); d != 0 {
		return d
	}
	if d := cmp.Compare(a.dr, b.dr); d != 0 {
		return d
	}
	if d := cmp.Compare(a.dc, b.dc); d != 0 {
		return d
	}
	return cmp.Compare(a.tileID, b.tileID)
}

// retainMove 维护一个以最差候选为堆顶的最大堆。搜索只需要前 beam 个着法，
// 因而不必为可能大得多的完整着法列表分配空间。
func retainMove(moves []searchMove, move searchMove, limit int) []searchMove {
	if limit <= 0 {
		return append(moves, move)
	}
	if len(moves) < limit {
		moves = append(moves, move)
		for i := len(moves) - 1; i > 0; {
			parent := (i - 1) / 2
			if compareSearchMove(moves[parent], moves[i]) >= 0 {
				break
			}
			moves[parent], moves[i] = moves[i], moves[parent]
			i = parent
		}
		return moves
	}
	if compareSearchMove(move, moves[0]) >= 0 {
		return moves
	}
	moves[0] = move
	for i := 0; ; {
		left := i*2 + 1
		if left >= len(moves) {
			break
		}
		worst := left
		right := left + 1
		if right < len(moves) && compareSearchMove(moves[right], moves[left]) > 0 {
			worst = right
		}
		if compareSearchMove(moves[i], moves[worst]) >= 0 {
			break
		}
		moves[i], moves[worst] = moves[worst], moves[i]
		i = worst
	}
	return moves
}

func enumSearchMovesWithWorkspace(
	b *compactBoard,
	rows, cols int,
	dst []searchMove,
	limit int,
	workspace *moveEnumWorkspace,
) []searchMove {
	n := rows * cols
	workspace.build(b, rows, cols)
	var counts [256]uint8
	for i := 0; i < n; i++ {
		v := b[i]
		if v != Empty {
			counts[uint8(v)]++
		}
	}
	moves := dst[:0]
	if limit > 0 && cap(moves) < limit {
		moves = make([]searchMove, 0, limit)
	}
	flat := b[:n]
	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			tile := at(flat, cols, row, col)
			if tile == Empty {
				continue
			}
			for di, d := range dirs {
				dr, dc := d[0], d[1]
				maxShift := int(workspace.shift[row*cols+col][di])
				if shiftPolicy == "horizontal" && dr != 0 {
					maxShift = 0
				}
				for shift := 0; shift <= maxShift; shift++ {
					// 零位移着法与外层推动方向无关。旧实现会先构造四个相同候选，
					// 再通过 seen map 去重；这里直接跳过重复方向。
					if shift == 0 && di != 0 {
						continue
					}
					lr, lc := row+dr*shift, col+dc*shift
					tr, tc, ok := findSameIndexed(b, cols, row, col, lr, lc, tile, workspace)
					if !ok || (tr == row && tc == col) {
						continue
					}
					if blockedByRayIndexed(cols, row, col, tr, tc, dr, dc, di, workspace) {
						continue
					}
					if shift == 0 && row*cols+col > tr*cols+tc {
						continue
					}
					dist := abs(row-tr) + abs(col-tc)
					rank := uint32(shift)<<16 | uint32(counts[uint8(tile)])<<8 | uint32(dist)
					moves = retainMove(moves, searchMove{
						tileID: tile,
						sr:     uint8(row), sc: uint8(col),
						tr: uint8(tr), tc: uint8(tc),
						lr: uint8(lr), lc: uint8(lc),
						dr: int8(dr), dc: int8(dc),
						shift: uint8(shift), rank: rank,
					}, limit)
				}
			}
		}
	}
	slices.SortFunc(moves, compareSearchMove)
	return moves
}

func enumSearchMoves(b *compactBoard, rows, cols int, dst []searchMove, limit int) []searchMove {
	var workspace moveEnumWorkspace
	return enumSearchMovesWithWorkspace(b, rows, cols, dst, limit, &workspace)
}

func enumMoves(b []int8, rows, cols int) []Move {
	var board compactBoard
	copy(board[:], b[:rows*cols])
	var workspace moveEnumWorkspace
	compact := enumSearchMovesWithWorkspace(
		&board, rows, cols, make([]searchMove, 0, 64), 0, &workspace,
	)
	out := make([]Move, len(compact))
	for i, move := range compact {
		out[i] = move.public()
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
