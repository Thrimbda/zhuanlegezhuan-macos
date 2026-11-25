package main

import (
	"bufio"
	"encoding/json"
	"os"
	"runtime"

	"zlgz"
)

type request struct {
	Cmd      string `json:"cmd"`
	Board    []int8 `json:"board"`
	Rows     int    `json:"rows"`
	Cols     int    `json:"cols"`
	MaxMs    int    `json:"max_ms"`
	MaxNodes int    `json:"max_nodes"`
	Beam     int    `json:"beam"`
	Depth    int    `json:"depth"`
}

type moveJSON struct {
	TileID int `json:"tile_id"`
	SR     int `json:"sr"`
	SC     int `json:"sc"`
	TR     int `json:"tr"`
	TC     int `json:"tc"`
	LR     int `json:"lr"`
	LC     int `json:"lc"`
	DR     int `json:"dr"`
	DC     int `json:"dc"`
	Shift  int `json:"shift"`
}

type response struct {
	OK        bool       `json:"ok"`
	Solved    bool       `json:"solved"`
	Reason    string     `json:"reason"`
	Nodes     int        `json:"nodes"`
	ElapsedMs int        `json:"elapsed_ms"`
	Moves     []moveJSON `json:"moves"`
	Error     string     `json:"error,omitempty"`
}

func pack(m zlgz.Move) moveJSON {
	return moveJSON{
		TileID: m.TileID,
		SR:     m.SR, SC: m.SC,
		TR: m.TR, TC: m.TC,
		LR: m.LR, LC: m.LC,
		DR: m.DR, DC: m.DC,
		Shift: m.Shift,
	}
}

func packAll(ms []zlgz.Move) []moveJSON {
	out := make([]moveJSON, len(ms))
	for i, m := range ms {
		out[i] = pack(m)
	}
	return out
}

func handle(req request) response {
	n := req.Rows * req.Cols
	if req.Rows <= 0 || req.Cols <= 0 || n > 160 || len(req.Board) < n {
		return response{Error: "bad board"}
	}
	board := req.Board[:n]
	beam := req.Beam
	switch req.Cmd {
	case "solve":
		maxMs := req.MaxMs
		maxNodes := req.MaxNodes
		if beam <= 0 {
			beam = zlgz.SolverBeam
		}
		got := zlgz.Solve(board, req.Rows, req.Cols, maxMs, maxNodes, beam)
		return response{
			OK:        true,
			Solved:    got.Solved,
			Reason:    got.Reason,
			Nodes:     got.Nodes,
			ElapsedMs: got.ElapsedMs,
			Moves:     packAll(got.Steps),
		}
	case "lookahead":
		depth := req.Depth
		if depth <= 0 {
			depth = zlgz.SolverDepth
		}
		if beam <= 0 {
			beam = zlgz.SolverBeam
		}
		move, ok := zlgz.Lookahead(board, req.Rows, req.Cols, depth, beam)
		if !ok {
			return response{OK: true, Solved: false, Reason: "none", Moves: []moveJSON{}}
		}
		return response{OK: true, Solved: true, Reason: "ok", Moves: []moveJSON{pack(move)}}
	default:
		return response{Error: "unknown cmd"}
	}
}

func main() {
	// 原生求解器是独立进程，固定使用 12 个计算执行单元。
	runtime.GOMAXPROCS(zlgz.SolverWorkers)
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(out)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(response{Error: err.Error()})
			_ = out.Flush()
			continue
		}
		_ = enc.Encode(handle(req))
		_ = out.Flush()
	}
}
