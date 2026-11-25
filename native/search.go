package zlgz

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	// SolverWorkers 是原生求解器固定使用的并行 worker 数。
	SolverWorkers = 12
	// SolverBeam 是迭代扩宽的首轮候选宽度；失败后持续翻倍。
	SolverBeam = 48
	// SolverDepth 是引导式预谋的最小深度。
	SolverDepth = 40
)

type SolveResult struct {
	Solved    bool
	Steps     []Move
	Nodes     int
	Reason    string
	ElapsedMs int
}

// stateHash 是 128 位 Zobrist 风格指纹，将置换表键从 160 字节压到 16 字节。
// 两条独立混合通道让意外碰撞概率可以忽略，同时支持增量更新。
type stateHash struct {
	a uint64
	b uint64
}

func mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func cellHash(index int, value int8) stateHash {
	x := uint64(index+1)<<8 | uint64(uint8(value))
	return stateHash{
		a: mix64(x + 0x9e3779b97f4a7c15),
		b: mix64(x + 0xd1b54a32d192ed03),
	}
}

func hashBoard(board *compactBoard, n int) stateHash {
	var out stateHash
	for i := 0; i < n; i++ {
		if board[i] == Empty {
			continue
		}
		part := cellHash(i, board[i])
		out.a ^= part.a
		out.b ^= part.b
	}
	return out
}

func setHashed(board *compactBoard, index int, value int8, hash *stateHash) {
	old := board[index]
	if old == value {
		return
	}
	if old != Empty {
		part := cellHash(index, old)
		hash.a ^= part.a
		hash.b ^= part.b
	}
	if value != Empty {
		part := cellHash(index, value)
		hash.a ^= part.a
		hash.b ^= part.b
	}
	board[index] = value
}

func applySearchMove(
	board *compactBoard,
	rows, cols int,
	move searchMove,
	hash stateHash,
) (stateHash, bool) {
	sr, sc := int(move.sr), int(move.sc)
	tr, tc := int(move.tr), int(move.tc)
	setHashed(board, sr*cols+sc, Empty, &hash)
	setHashed(board, tr*cols+tc, Empty, &hash)
	if move.shift == 0 {
		return hash, true
	}

	dr, dc := int(move.dr), int(move.dc)
	er, ec := findEnd(board[:rows*cols], rows, cols, sr, sc, dr, dc)
	cr, cc := er, ec
	for cr != sr || cc != sc {
		nr := cr + dr*int(move.shift)
		nc := cc + dc*int(move.shift)
		if !inside(nr, nc, rows, cols) {
			return stateHash{}, false
		}
		from, to := cr*cols+cc, nr*cols+nc
		value := board[from]
		setHashed(board, to, value, &hash)
		setHashed(board, from, Empty, &hash)
		cr -= dr
		cc -= dc
	}
	return hash, true
}

type dfsSearch struct {
	rows, cols int
	beam       int
	maxNodes   int
	maxMs      int
	started    time.Time
	nodes      int
	timedOut   bool
	nodeLimit  bool
	memo       map[stateHash]struct{}
	memoLimit  int
	path       []searchMove
	moveBuf    [maxCells/2 + 1][]searchMove
	enum       moveEnumWorkspace
	control    *parallelControl
}

const (
	parallelRunning int32 = iota
	parallelSolved
	parallelTimedOut
	parallelNodeLimited
)

type parallelControl struct {
	nodes    atomic.Int64
	state    atomic.Int32
	maxNodes int64
	deadline time.Time
}

func (control *parallelControl) cancelled() bool {
	return control.state.Load() != parallelRunning
}

func (control *parallelControl) stop(reason int32) bool {
	return control.state.CompareAndSwap(parallelRunning, reason)
}

func (control *parallelControl) visit() bool {
	if control.cancelled() {
		return true
	}
	nodes := control.nodes.Add(1)
	if control.maxNodes > 0 && nodes >= control.maxNodes {
		control.stop(parallelNodeLimited)
		return true
	}
	if !control.deadline.IsZero() && (nodes == 1 || nodes&63 == 0) &&
		time.Now().After(control.deadline) {
		control.stop(parallelTimedOut)
		return true
	}
	return control.cancelled()
}

func newDFSSearch(rows, cols, maxMs, maxNodes, beam int) *dfsSearch {
	memoLimit := maxNodes
	if memoLimit <= 0 || memoLimit > 262_144 {
		memoLimit = 262_144
	}
	initial := memoLimit
	if initial > 256 {
		initial = 256
	}
	return &dfsSearch{
		rows: rows, cols: cols, beam: beam,
		maxNodes: maxNodes, maxMs: maxMs, started: time.Now(),
		memo: make(map[stateHash]struct{}, initial), memoLimit: memoLimit,
		path: make([]searchMove, 0, rows*cols/2),
	}
}

func newParallelDFSSearch(
	rows, cols, beam, memoLimit int,
	control *parallelControl,
) *dfsSearch {
	if memoLimit < 1 {
		memoLimit = 1
	}
	initial := memoLimit
	if initial > 256 {
		initial = 256
	}
	return &dfsSearch{
		rows: rows, cols: cols, beam: beam,
		memo: make(map[stateHash]struct{}, initial), memoLimit: memoLimit,
		path:    make([]searchMove, 0, rows*cols/2),
		control: control,
	}
}

func (search *dfsSearch) stopped() bool {
	if search.control != nil {
		return search.control.cancelled()
	}
	if search.maxNodes > 0 && search.nodes >= search.maxNodes {
		search.nodeLimit = true
		return true
	}
	// 紧密 DFS 中读取时钟的成本不可忽视。每 64 个节点检查一次，既能及时响应
	// 截止时间，也避免每个节点都读取时钟。
	if search.maxMs > 0 && (search.nodes == 1 || search.nodes&63 == 0) &&
		time.Since(search.started).Milliseconds() > int64(search.maxMs) {
		search.timedOut = true
		return true
	}
	return false
}

func (search *dfsSearch) aborted() bool {
	if search.control != nil {
		return search.control.cancelled()
	}
	return search.timedOut || search.nodeLimit
}

func (search *dfsSearch) rememberDead(hash stateHash) {
	if len(search.memo) >= search.memoLimit {
		// 复用已经分配的桶。分代清空既固定内存上限，也让最近状态继续发挥作用。
		clear(search.memo)
	}
	search.memo[hash] = struct{}{}
}

func (search *dfsSearch) dfs(
	board compactBoard,
	left int,
	depth int,
	hash stateHash,
) bool {
	search.nodes++
	if search.control != nil {
		if search.control.visit() {
			return false
		}
	} else if search.stopped() {
		return false
	}
	if left == 0 {
		return true
	}
	if _, hit := search.memo[hash]; hit {
		return false
	}

	moves := enumSearchMovesWithWorkspace(
		&board, search.rows, search.cols, search.moveBuf[depth][:0], search.beam,
		&search.enum,
	)
	search.moveBuf[depth] = moves
	if len(moves) == 0 {
		search.rememberDead(hash)
		return false
	}
	if search.beam > 0 && len(moves) > search.beam {
		moves = moves[:search.beam]
	}
	for i := range moves {
		move := moves[i]
		child := board
		childHash, ok := applySearchMove(&child, search.rows, search.cols, move, hash)
		if !ok {
			continue
		}
		search.path = append(search.path, move)
		if search.dfs(child, left-2, depth+1, childHash) {
			return true
		}
		search.path = search.path[:len(search.path)-1]
		if search.aborted() {
			return false
		}
	}
	if search.aborted() {
		return false
	}
	search.rememberDead(hash)
	return false
}

type dfsRootTask struct {
	board compactBoard
	hash  stateHash
	path  []searchMove
	left  int
	depth int
}

func parallelDFS(
	root compactBoard,
	left int,
	rows, cols, maxMs, maxNodes, beam, workers int,
	started time.Time,
) ([]Move, int, string) {
	if left == 0 {
		return []Move{}, 1, "solved"
	}
	control := &parallelControl{maxNodes: int64(maxNodes)}
	if maxMs > 0 {
		control.deadline = started.Add(time.Duration(maxMs) * time.Millisecond)
	}
	rootHash := hashBoard(&root, rows*cols)
	tasks := []dfsRootTask{{board: root, hash: rootHash, left: left}}
	var enumWorkspace moveEnumWorkspace
	for len(tasks) < workers {
		next := make([]dfsRootTask, 0, workers*beam)
		for _, task := range tasks {
			if control.visit() {
				break
			}
			moves := enumSearchMovesWithWorkspace(
				&task.board, rows, cols, nil, beam, &enumWorkspace,
			)
			for _, move := range moves {
				child := task.board
				childHash, ok := applySearchMove(&child, rows, cols, move, task.hash)
				if !ok {
					continue
				}
				path := make([]searchMove, len(task.path)+1)
				copy(path, task.path)
				path[len(task.path)] = move
				childLeft := task.left - 2
				if childLeft == 0 {
					steps := make([]Move, len(path))
					for i, item := range path {
						steps[i] = item.public()
					}
					return steps, int(control.nodes.Load()), "solved"
				}
				next = append(next, dfsRootTask{
					board: child, hash: childHash, path: path,
					left: childLeft, depth: task.depth + 1,
				})
			}
		}
		if control.cancelled() {
			nodes := int(control.nodes.Load())
			if control.state.Load() == parallelTimedOut {
				return nil, nodes, "timeout"
			}
			return nil, nodes, "nodes"
		}
		if len(next) == 0 {
			return nil, int(control.nodes.Load()), "unsolved"
		}
		tasks = next
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}

	memoTotal := maxNodes
	if memoTotal <= 0 || memoTotal > 262_144 {
		memoTotal = 262_144
	}
	memoPerWorker := (memoTotal + workers - 1) / workers
	taskChannel := make(chan dfsRootTask, len(tasks))
	for _, task := range tasks {
		taskChannel <- task
	}
	close(taskChannel)
	solutionChannel := make(chan []searchMove, 1)
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wait.Done()
			search := newParallelDFSSearch(rows, cols, beam, memoPerWorker, control)
			for task := range taskChannel {
				if control.cancelled() {
					return
				}
				search.path = append(search.path[:0], task.path...)
				if search.dfs(task.board, task.left, task.depth, task.hash) {
					path := append([]searchMove(nil), search.path...)
					if control.stop(parallelSolved) {
						solutionChannel <- path
					}
					return
				}
				search.path = search.path[:0]
				if control.cancelled() {
					return
				}
			}
		}()
	}
	wait.Wait()
	nodes := int(control.nodes.Load())
	if control.state.Load() == parallelSolved {
		path := <-solutionChannel
		steps := make([]Move, len(path))
		for i, move := range path {
			steps[i] = move.public()
		}
		return steps, nodes, "solved"
	}
	switch control.state.Load() {
	case parallelTimedOut:
		return nil, nodes, "timeout"
	case parallelNodeLimited:
		return nil, nodes, "nodes"
	default:
		return nil, nodes, "unsolved"
	}
}

func Solve(board []int8, rows, cols, maxMs, maxNodes, beam int) SolveResult {
	started := time.Now()
	// 保留 beam 参数以兼容现有调用协议；整盘求解固定从 48 开始迭代扩宽。
	_ = beam
	n := rows * cols
	if oddTypes(board, n) {
		return SolveResult{Reason: "odd"}
	}
	// 从较窄的 beam 开始，失败后逐轮翻倍。简单局面无需直接承担大 beam
	// 的时间和内存成本；搜索没有固定宽度上限，也不再进入全量 DFS。
	guidedNodes := 0
	for attemptBeam := SolverBeam; ; attemptBeam *= 2 {
		steps, ok, pruned := guidedSolveAttempt(
			board, rows, cols, attemptBeam, SolverWorkers, &guidedNodes,
		)
		if ok {
			return SolveResult{
				Solved: true, Steps: steps, Nodes: guidedNodes,
				Reason: "solved", ElapsedMs: int(time.Since(started).Milliseconds()),
			}
		}
		if maxNodes > 0 && guidedNodes >= maxNodes {
			return SolveResult{
				Nodes: guidedNodes, Reason: "nodes",
				ElapsedMs: int(time.Since(started).Milliseconds()),
			}
		}
		if maxMs > 0 && time.Since(started).Milliseconds() >= int64(maxMs) {
			return SolveResult{
				Nodes: guidedNodes, Reason: "timeout",
				ElapsedMs: int(time.Since(started).Milliseconds()),
			}
		}
		// 本轮从未因宽度丢弃不同局面，说明 beam 已经覆盖实际前沿；继续
		// 翻倍只会得到相同结果。这个自然终止条件不是固定 beam 上限。
		if !pruned {
			return SolveResult{
				Nodes: guidedNodes, Reason: "unsolved",
				ElapsedMs: int(time.Since(started).Milliseconds()),
			}
		}
		if attemptBeam > int(^uint(0)>>1)/2 {
			return SolveResult{
				Nodes: guidedNodes, Reason: "unsolved",
				ElapsedMs: int(time.Since(started).Milliseconds()),
			}
		}
	}
}

func guidedSolve(
	board []int8,
	rows, cols, beam int,
	workers int,
	work *int,
) ([]Move, bool) {
	steps, ok, _ := guidedSolveAttempt(board, rows, cols, beam, workers, work)
	return steps, ok
}

func guidedSolveAttempt(
	board []int8,
	rows, cols, beam int,
	workers int,
	work *int,
) ([]Move, bool, bool) {
	if beam <= 0 {
		beam = SolverBeam
	}
	state := append([]int8(nil), board[:rows*cols]...)
	steps := make([]Move, 0, len(state)/2)
	workspace := newLookaheadWorkspace(workers, beam)
	for remaining(state, len(state)) > 0 {
		move, ok := lookaheadWithWorkspace(state, rows, cols, SolverDepth, workspace, work)
		if !ok {
			return nil, false, workspace.pruned
		}
		next, ok := applyMove(state, rows, cols, move)
		if !ok {
			return nil, false, workspace.pruned
		}
		state = next
		steps = append(steps, move)
	}
	return steps, true, workspace.pruned
}

type frontierItem struct {
	board    compactBoard
	hash     stateHash
	first    searchMove
	hasFirst bool
	direct   int
	pathRank uint64
}

func directPairCount(board *compactBoard, rows, cols int) int {
	count := 0
	for row := 0; row < rows; row++ {
		previous := Empty
		for col := 0; col < cols; col++ {
			value := board[row*cols+col]
			if value == Empty {
				continue
			}
			if previous == value {
				count++
			}
			previous = value
		}
	}
	for col := 0; col < cols; col++ {
		previous := Empty
		for row := 0; row < rows; row++ {
			value := board[row*cols+col]
			if value == Empty {
				continue
			}
			if previous == value {
				count++
			}
			previous = value
		}
	}
	return count
}

func betterFrontier(a, b frontierItem) bool {
	if a.direct != b.direct {
		return a.direct > b.direct
	}
	if a.pathRank != b.pathRank {
		return a.pathRank < b.pathRank
	}
	if a.hash.a != b.hash.a {
		return a.hash.a < b.hash.a
	}
	return a.hash.b < b.hash.b
}

// frontierAtCapacity 判断加入一个不同局面是否必然挤掉另一个局面。重复哈希
// 只是在同一局面中保留更好的路径，不表示 beam 宽度发生了截断。
func frontierAtCapacity(items []frontierItem, candidate frontierItem, limit int) bool {
	if limit <= 0 || len(items) < limit {
		return false
	}
	for i := range items {
		if items[i].hash == candidate.hash {
			return false
		}
	}
	return true
}

// insertFrontier 只保留最好的 beam 个候选，并合并 beam 内已有的同局面状态，
// 避免可交换的消除顺序让同一棋盘占用多个名额。
func insertFrontier(items []frontierItem, candidate frontierItem, limit int) []frontierItem {
	for i := range items {
		if items[i].hash == candidate.hash {
			if !betterFrontier(candidate, items[i]) {
				return items
			}
			items[i] = candidate
			for i > 0 && betterFrontier(items[i], items[i-1]) {
				items[i], items[i-1] = items[i-1], items[i]
				i--
			}
			return items
		}
	}
	if len(items) < limit {
		items = append(items, candidate)
		i := len(items) - 1
		for i > 0 && betterFrontier(items[i], items[i-1]) {
			items[i], items[i-1] = items[i-1], items[i]
			i--
		}
		return items
	}
	if limit == 0 || !betterFrontier(candidate, items[len(items)-1]) {
		return items
	}
	items[len(items)-1] = candidate
	for i := len(items) - 1; i > 0 && betterFrontier(items[i], items[i-1]); i-- {
		items[i], items[i-1] = items[i-1], items[i]
	}
	return items
}

func Lookahead(board []int8, rows, cols, depth, beam int) (Move, bool) {
	if depth < SolverDepth {
		depth = SolverDepth
	}
	workspace := newLookaheadWorkspace(SolverWorkers, beam)
	return lookaheadWithWorkspace(board, rows, cols, depth, workspace, nil)
}

type lookaheadWorkspace struct {
	workers         int
	beam            int
	frontier        []frontierItem
	next            []frontierItem
	workerFrontiers [][]frontierItem
	workerMoves     [][]searchMove
	workerEnums     []moveEnumWorkspace
	workerSolutions []searchMove
	workerSolved    []bool
	workerPruned    []bool
	workerWork      []int
	pruned          bool
}

func newLookaheadWorkspace(workers, beam int) *lookaheadWorkspace {
	if beam < SolverBeam {
		beam = SolverBeam
	}
	if workers <= 0 {
		workers = 1
	}
	if workers > SolverWorkers {
		workers = SolverWorkers
	}
	workspace := &lookaheadWorkspace{
		workers:         workers,
		beam:            beam,
		frontier:        make([]frontierItem, 0, beam),
		next:            make([]frontierItem, 0, beam),
		workerFrontiers: make([][]frontierItem, workers),
		workerMoves:     make([][]searchMove, workers),
		workerEnums:     make([]moveEnumWorkspace, workers),
		workerSolutions: make([]searchMove, workers),
		workerSolved:    make([]bool, workers),
		workerPruned:    make([]bool, workers),
		workerWork:      make([]int, workers),
	}
	for i := 0; i < workers; i++ {
		workspace.workerFrontiers[i] = make([]frontierItem, 0, beam)
		workspace.workerMoves[i] = make([]searchMove, 0, beam)
	}
	return workspace
}

func lookaheadWithWorkspace(
	board []int8,
	rows, cols, depth int,
	workspace *lookaheadWorkspace,
	work *int,
) (Move, bool) {
	n := rows * cols
	left := remaining(board, n)
	if left == 0 {
		return Move{}, false
	}
	beam := workspace.beam
	workers := workspace.workers
	var root compactBoard
	copy(root[:], board[:n])
	rootHash := hashBoard(&root, n)
	frontier := workspace.frontier[:1]
	frontier[0] = frontierItem{board: root, hash: rootHash}
	next := workspace.next[:0]
	workerFrontiers := workspace.workerFrontiers
	workerMoves := workspace.workerMoves
	workerEnums := workspace.workerEnums
	workerSolutions := workspace.workerSolutions
	workerSolved := workspace.workerSolved
	workerPruned := workspace.workerPruned
	workerWork := workspace.workerWork

	for level := 0; level < depth; level++ {
		next = next[:0]
		active := workers
		if active > len(frontier) {
			active = len(frontier)
		}
		for i := 0; i < active; i++ {
			workerFrontiers[i] = workerFrontiers[i][:0]
			workerSolved[i] = false
			workerPruned[i] = false
			workerWork[i] = 0
		}
		var wait sync.WaitGroup
		wait.Add(active)
		for worker := 0; worker < active; worker++ {
			go func(worker int) {
				defer wait.Done()
				localNext := workerFrontiers[worker]
				localMoves := workerMoves[worker]
				for index := worker; index < len(frontier); index += active {
					item := frontier[index]
					localMoves = enumSearchMovesWithWorkspace(
						&item.board, rows, cols, localMoves[:0], beam, &workerEnums[worker],
					)
					for _, move := range localMoves {
						workerWork[worker]++
						child := item.board
						childHash, ok := applySearchMove(&child, rows, cols, move, item.hash)
						if !ok {
							continue
						}
						first, hasFirst := item.first, item.hasFirst
						if !hasFirst {
							first, hasFirst = move, true
						}
						if left-(level+1)*2 == 0 {
							workerSolutions[worker] = first
							workerSolved[worker] = true
							break
						}
						candidate := frontierItem{
							board: child, hash: childHash,
							first: first, hasFirst: hasFirst,
							direct:   directPairCount(&child, rows, cols),
							pathRank: item.pathRank + uint64(move.rank),
						}
						if frontierAtCapacity(localNext, candidate, beam) {
							workerPruned[worker] = true
						}
						localNext = insertFrontier(localNext, candidate, beam)
					}
					if workerSolved[worker] {
						break
					}
				}
				workerFrontiers[worker] = localNext
				workerMoves[worker] = localMoves
			}(worker)
		}
		wait.Wait()
		for worker := 0; worker < active; worker++ {
			if work != nil {
				*work += workerWork[worker]
			}
			if workerSolved[worker] {
				return workerSolutions[worker].public(), true
			}
			if workerPruned[worker] {
				workspace.pruned = true
			}
			for _, candidate := range workerFrontiers[worker] {
				if frontierAtCapacity(next, candidate, beam) {
					workspace.pruned = true
				}
				next = insertFrontier(next, candidate, beam)
			}
		}
		if len(next) == 0 {
			break
		}
		frontier, next = next, frontier[:0]
	}
	if len(frontier) > 0 && frontier[0].hasFirst {
		return frontier[0].first.public(), true
	}
	moves := enumSearchMovesWithWorkspace(
		&root, rows, cols, workerMoves[0][:0], beam, &workerEnums[0],
	)
	if len(moves) == 0 {
		return Move{}, false
	}
	return moves[0].public(), true
}
