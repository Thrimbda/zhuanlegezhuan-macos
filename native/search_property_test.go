package zlgz

import (
	"math/rand"
	"testing"
)

func randomPairedBoard(rng *rand.Rand, rows, cols, pairs int) compactBoard {
	var board compactBoard
	for i := range board {
		board[i] = Empty
	}
	positions := rng.Perm(rows * cols)
	for pair := 0; pair < pairs; pair++ {
		tile := int8(pair % 12)
		board[positions[pair*2]] = tile
		board[positions[pair*2+1]] = tile
	}
	return board
}

func TestSearchMoveLimitMatchesFullPrefix(t *testing.T) {
	rng := rand.New(rand.NewSource(20260828))
	for trial := 0; trial < 80; trial++ {
		board := randomPairedBoard(rng, 14, 10, 8+rng.Intn(35))
		full := enumSearchMoves(&board, 14, 10, nil, 0)
		for _, limit := range []int{1, 8, 24, 48} {
			limited := enumSearchMoves(&board, 14, 10, nil, limit)
			want := len(full)
			if want > limit {
				want = limit
			}
			if len(limited) != want {
				t.Fatalf("trial=%d limit=%d: got %d moves, want %d", trial, limit, len(limited), want)
			}
			for i := range limited {
				if limited[i] != full[i] {
					t.Fatalf("trial=%d limit=%d index=%d: got %+v, want %+v", trial, limit, i, limited[i], full[i])
				}
			}
		}
	}
}

func TestIndexedVisibilityMatchesRayScan(t *testing.T) {
	rng := rand.New(rand.NewSource(20260829))
	for trial := 0; trial < 120; trial++ {
		board := randomPairedBoard(rng, 14, 10, 4+rng.Intn(45))
		var workspace moveEnumWorkspace
		workspace.build(&board, 14, 10)
		for row := 0; row < 14; row++ {
			for col := 0; col < 10; col++ {
				tile := board[row*10+col]
				if tile == Empty {
					continue
				}
				for direction, delta := range dirs {
					dr, dc := delta[0], delta[1]
					capacity := shiftCapacity(board[:140], 14, 10, row, col, dr, dc)
					if indexed := int(workspace.shift[row*10+col][direction]); indexed != capacity {
						t.Fatalf(
							"trial=%d source=(%d,%d) dir=%d: capacity=%d want=%d",
							trial, row, col, direction, indexed, capacity,
						)
					}
					for shift := 0; shift <= capacity; shift++ {
						candidateRow := row + dr*shift
						candidateCol := col + dc*shift
						wantRow, wantCol, wantOK := findSame(
							board[:140], 14, 10, row, col, candidateRow, candidateCol, tile,
						)
						gotRow, gotCol, gotOK := findSameIndexed(
							&board, 10, row, col, candidateRow, candidateCol, tile, &workspace,
						)
						if gotOK != wantOK || gotRow != wantRow || gotCol != wantCol {
							t.Fatalf(
								"trial=%d source=(%d,%d) dir=%d shift=%d: got=(%d,%d,%v) want=(%d,%d,%v)",
								trial, row, col, direction, shift,
								gotRow, gotCol, gotOK, wantRow, wantCol, wantOK,
							)
						}
						if wantOK {
							wantBlocked := blockedByRay(
								board[:140], 14, 10, row, col, wantRow, wantCol, dr, dc,
							)
							gotBlocked := blockedByRayIndexed(
								10, row, col, gotRow, gotCol, dr, dc, direction, &workspace,
							)
							if gotBlocked != wantBlocked {
								t.Fatalf(
									"trial=%d source=(%d,%d) target=(%d,%d): blocked=%v want=%v",
									trial, row, col, gotRow, gotCol, gotBlocked, wantBlocked,
								)
							}
						}
					}
				}
			}
		}
	}
}

func TestCompactApplyAndHashMatchPublicBoard(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 60; trial++ {
		board := randomPairedBoard(rng, 14, 10, 6+rng.Intn(30))
		moves := enumSearchMoves(&board, 14, 10, nil, 0)
		for _, move := range moves {
			want, ok := applyMove(board[:140], 14, 10, move.public())
			if !ok {
				t.Fatalf("枚举出的着法无法由公开实现执行: %+v", move)
			}
			got := board
			gotHash, ok := applySearchMove(&got, 14, 10, move, hashBoard(&board, 140))
			if !ok {
				t.Fatalf("枚举出的着法无法由紧凑实现执行: %+v", move)
			}
			for i := 0; i < 140; i++ {
				if got[i] != want[i] {
					t.Fatalf("trial=%d cell=%d move=%+v: got %d, want %d", trial, i, move, got[i], want[i])
				}
			}
			if wantHash := hashBoard(&got, 140); gotHash != wantHash {
				t.Fatalf("trial=%d move=%+v: 增量哈希不一致", trial, move)
			}
		}
	}
}

func bruteSolvable(board []int8, rows, cols int, memo map[[16]int8]bool) bool {
	if remaining(board, rows*cols) == 0 {
		return true
	}
	var key [16]int8
	copy(key[:], board)
	if memo[key] {
		return false
	}
	for _, move := range enumMoves(board, rows, cols) {
		child, ok := applyMove(board, rows, cols, move)
		if ok && bruteSolvable(child, rows, cols, memo) {
			return true
		}
	}
	memo[key] = true
	return false
}

func TestSolveMatchesBruteForceOnSmallBoards(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 50; trial++ {
		board := randomPairedBoard(rng, 4, 4, 3+rng.Intn(4))
		flat := append([]int8(nil), board[:16]...)
		want := bruteSolvable(flat, 4, 4, make(map[[16]int8]bool))
		got := Solve(flat, 4, 4, 0, 0, 0)
		if got.Solved != want {
			t.Fatalf("trial=%d: solved=%v, want %v, result=%+v, board=%v", trial, got.Solved, want, got, flat)
		}
	}
}
