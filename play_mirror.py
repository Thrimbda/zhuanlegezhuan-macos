"""Play one board in iPhone Mirroring or Mac WeChat with the upstream Go solver."""
import argparse
from collections import Counter
from contextlib import contextmanager
from io import BytesIO
from itertools import count
import json
import subprocess
import time
from pathlib import Path

from PIL import Image, ImageChops

ROOT = Path(__file__).resolve().parent
BIN = ROOT / "bin"
LOCAL = ROOT / ".local"
MIRROR_SIZE = (300, 663)
WECHAT_SIZE = (429, 805)
ROWS, COLS = 14, 10
EMPTY = -1


def point(index, wechat=False):
    row, col = divmod(index, COLS)
    if wechat:
        return round(33 + 40.2 * col), round(155.8 + 40.6 * row)
    return round(29.5 + 26.75 * col), round(198.5 + 26.85 * row)


def patches(image, wechat=False):
    size = WECHAT_SIZE if wechat else MIRROR_SIZE
    if image.size != size:
        raise RuntimeError(f"Window size changed to {image.size}; expected {size}")
    image = image.convert("RGB")
    centers = [point(i, wechat) for i in range(ROWS * COLS)]
    if wechat:
        return [image.resize((18, 18), Image.Resampling.BILINEAR,
                             box=(x - 13.5, y - 13.5, x + 13.5, y + 13.5))
                for x, y in centers]
    return [image.crop((x - 9, y - 9, x + 9, y + 9)) for x, y in centers]


@contextmanager
def mirror_session(wechat=False):
    command = [BIN / "mirror", "--wechat", "serve"] if wechat else [BIN / "mirror", "serve"]
    with subprocess.Popen(command, stdin=subprocess.PIPE,
                          stdout=subprocess.PIPE) as mirror:
        try:
            yield mirror
        finally:
            mirror.stdin.close()


def exchange(mirror, *args):
    mirror.stdin.write((" ".join(map(str, args)) + "\n").encode("ascii"))
    mirror.stdin.flush()
    reply = mirror.stdout.readline().strip()
    if not reply:
        raise RuntimeError("Mirror helper exited")
    return reply


def frame(mirror, command):
    reply = exchange(mirror, command)
    if reply == b"WAIT":
        return None
    kind, size = reply.split()
    if kind != b"FRAME":
        raise RuntimeError(f"Unexpected frame reply: {reply!r}")
    return mirror.stdout.read(int(size))


def score(a, b):
    return sum(ImageChops.difference(a, b).tobytes()) / (a.width * a.height)


def read_board(data, samples, wechat=False):
    with Image.open(BytesIO(data)) as image:
        return recognize(patches(image, wechat), samples)


def confirm_board(mirror, samples, expected=None, wechat=False, *, picker=None):
    deadline = time.monotonic() + 2.5
    command = "latest" if expected is None else "frame"
    data = None
    board = None
    while time.monotonic() < deadline:
        newest = frame(mirror, command)
        if newest is None:
            time.sleep(0.03)
            continue
        data = newest
        board = read_board(data, samples, wechat)
        if (board == expected or (expected is None and None not in board)
                or match_picker_visible(board, picker, data)):
            break
    return board, data


def recognize(current, samples):
    result = []
    for tile in current:
        distance, label = min((score(tile, patch), label) for label, patch in samples)
        result.append(label if distance < 85 else None)
    return result


def index(move, key):
    return move[key + "r"] * COLS + move[key + "c"]


def apply_move(board, move):
    source, target = index(move, "s"), index(move, "t")
    assert board[source] == board[target] == move["tile_id"]
    result = board[:]
    result[source] = result[target] = EMPTY
    distance = move["shift"]
    if not distance:
        return result
    dr, dc = move["dr"], move["dc"]
    r, c = divmod(source, COLS)
    chain = []
    while True:
        r, c = r + dr, c + dc
        if not (0 <= r < ROWS and 0 <= c < COLS) or result[r * COLS + c] == EMPTY:
            break
        chain.append((r, c))
    for r, c in reversed(chain):
        result[(r + dr * distance) * COLS + c + dc * distance] = result[r * COLS + c]
        result[r * COLS + c] = EMPTY
    return result


def match_picker(expected, move):
    """Find other matches visible after sliding, before the chosen pair disappears."""
    target = index(move, "t")
    tile = move["tile_id"]
    row, col = divmod(index(move, "l"), COLS)
    alternatives = set()
    for dr, dc in ((-1, 0), (1, 0), (0, -1), (0, 1)):
        r, c = row + dr, col + dc
        while 0 <= r < ROWS and 0 <= c < COLS:
            cell = r * COLS + c
            # The game still shows the planned target in the picker.
            if cell == target:
                break
            if expected[cell] != EMPTY:
                if expected[cell] == tile:
                    alternatives.add(cell)
                break
            r, c = r + dr, c + dc
    return target, tile, alternatives


def match_picker_visible(board, picker, frame_data):
    if board is None or picker is None or board.count(None) <= len(board) // 2:
        return False
    target, tile, alternatives = picker
    if board[target] == tile and any(board[cell] == tile for cell in alternatives):
        return True
    # The game's pointer can cover the target icon. The highlighted card's corners
    # remain bright, and at least one other highlighted candidate still shows its icon.
    with Image.open(BytesIO(frame_data)) as image:
        image = image.convert("RGB")

    def highlighted(cell):
        x, y = point(cell, wechat=True)
        corners = (image.getpixel((x + dx, y + dy))
                   for dx, dy in ((-14, -14), (14, -14), (-14, 13), (14, 13)))
        return sum(r > 220 and g > 230 and b > 180 for r, g, b in corners) >= 3

    return highlighted(target) and any(
        label == tile and cell != target and highlighted(cell)
        for cell, label in enumerate(board)
    )


def verify_move(mirror, samples, expected, move, wechat=False):
    picker = match_picker(expected, move) if wechat else None
    actual, last_frame = confirm_board(mirror, samples, expected, wechat, picker=picker)
    if actual != expected and match_picker_visible(actual, picker, last_frame):
        target = index(move, "t")
        if exchange(mirror, "click", *point(target, wechat)) != b"OK":
            raise RuntimeError("Mirror helper did not acknowledge match selection")
        row, col = divmod(target, COLS)
        print(f"Selected match at row {row + 1}, column {col + 1}", flush=True)
        return confirm_board(mirror, samples, expected, wechat)
    return actual, last_frame


def submit(mirror, move, wechat=False):
    a, b = index(move, "s"), index(move, "t")
    adjacent = abs(move["sr"] - move["tr"]) + abs(move["sc"] - move["tc"]) == 1
    if move["shift"]:
        b = index(move, "l")
    mode = "click-pair" if adjacent and not move["shift"] else "drag"
    if exchange(mirror, mode, *point(a, wechat), *point(b, wechat)) != b"OK":
        raise RuntimeError("Mirror helper did not acknowledge input")


def solve(board, solver):
    request = dict(cmd="solve", board=board, rows=ROWS, cols=COLS, max_ms=20000)
    process = subprocess.run([BIN / solver], input=json.dumps(request) + "\n",
                             capture_output=True, text=True, check=True, timeout=60)
    plan = json.loads(process.stdout)
    if not plan["solved"]:
        raise RuntimeError(f"Solver stopped: {plan['reason']}")
    return plan["moves"]


def load_samples(prefix=""):
    label_path = LOCAL / f"{prefix}tile-labels.json"
    if not label_path.is_file():
        raise RuntimeError(f"Tile templates missing at {label_path}. See calibration in README.md.")
    labels = json.loads(label_path.read_text())["board"]
    with Image.open(LOCAL / f"{prefix}tile-samples.png") as atlas:
        samples = []
        for i, label in enumerate(labels):
            row, col = divmod(i, COLS)
            x, y = 18 * col, 18 * row
            samples.append((label, atlas.crop((x, y, x + 18, y + 18))))
    return samples


DIALOGS = {
    "reward": [
        ("wechat-reward-title.png", (143, 53, 286, 88)),
        ("wechat-reward-confirm.png", (157, 713, 272, 748)),
    ],
}


def load_dialogs():
    return {
        action: [(box, Image.open(LOCAL / name).convert("RGB")) for name, box in parts]
        for action, parts in DIALOGS.items()
    }


def dialog_action(image, dialogs):
    if image.size != WECHAT_SIZE:
        raise RuntimeError(f"Window size changed to {image.size}; expected {WECHAT_SIZE}")
    image = image.convert("RGB")
    for action, parts in dialogs.items():
        if all(score(image.crop(box), template) < 22 for box, template in parts):
            left, top, right, bottom = parts[-1][0]
            return action, ((left + right) // 2, (top + bottom) // 2)
    return None


def learn_new_tiles(board, cells, samples):
    unknown = {i for i, label in enumerate(board) if label is None}
    known = Counter(label for label in board if label is not None)
    # Learn only from a fresh, full board with enough recognized tiles to distinguish
    # the board from a dimmed overlay. Existing tile types must still make complete pairs.
    if not unknown or EMPTY in known or sum(known.values()) <= len(board) // 2:
        return None
    if any(count % 2 for count in known.values()):
        return None

    result = board[:]
    additions = []
    next_label = max(label for label, _ in samples) + 1
    max_distance = 45
    while unknown:
        first = min(unknown)
        group = {i for i in unknown if score(cells[first], cells[i]) < max_distance}
        if len(group) % 2:
            return None
        others = unknown - group
        if any(score(cells[a], cells[b]) >= max_distance for a in group for b in group):
            return None
        if any(score(cells[a], cells[b]) < max_distance for a in group for b in others):
            return None
        for i in sorted(group):
            result[i] = next_label
            additions.append((next_label, cells[i]))
        next_label += 1
        unknown = others
    # The upstream Go solver uses int8 tile IDs.
    if next_label > 128:
        return None
    return result, additions


def next_wechat_board(mirror, samples, dialogs, debug_path, *, after_clear=False):
    pressed = set()
    deadline = time.monotonic() + 20
    last_frame = None
    while time.monotonic() < deadline:
        data = frame(mirror, "frame")
        if data is not None:
            last_frame = data
            with Image.open(BytesIO(data)) as image:
                dialog = dialog_action(image, dialogs)
                if dialog is None and (not after_clear or pressed):
                    cells = patches(image, wechat=True)
                    board = recognize(cells, samples)
                    tiles = Counter(label for label in board if label != EMPTY)
                    if None not in board and tiles and all(count % 2 == 0 for count in tiles.values()):
                        return board
                    learned = learn_new_tiles(board, cells, samples)
                    if learned is not None:
                        board, additions = learned
                        samples.extend(additions)
                        print(f"Learned {len({label for label, _ in additions})} new tile types", flush=True)
                        return board
            if dialog is not None:
                action, target = dialog
                if action not in pressed:
                    if exchange(mirror, "click", *target) != b"OK":
                        raise RuntimeError("Mirror helper did not acknowledge next-level input")
                    print(f"Confirmed transition: {action}", flush=True)
                    pressed.add(action)
                    deadline = time.monotonic() + 20
        time.sleep(0.20)
    if last_frame is not None:
        debug_path.write_bytes(last_frame)
    raise RuntimeError(f"No verified next board. Transition clicks: {sorted(pressed)}")


def wait_for_reward(mirror, dialogs, debug_path):
    deadline = time.monotonic() + 20
    data = None
    while time.monotonic() < deadline:
        data = frame(mirror, "frame")
        if data is not None:
            with Image.open(BytesIO(data)) as image:
                action = dialog_action(image, dialogs)
                if action is not None and action[0] == "reward":
                    return
        time.sleep(0.2)
    if data is not None:
        debug_path.write_bytes(data)
    raise RuntimeError("Final reward screen was not confirmed")


def play_board(mirror, board, samples, args):
    moves = solve(board, "solve-horizontal" if args.horizontal else "solve")
    print(f"Board recognized, {len(moves)} pairs to clear", flush=True)
    if not args.execute:
        return
    for number, move in enumerate(moves[:args.limit], 1):
        expected = apply_move(board, move)
        adjacent = abs(move["sr"] - move["tr"]) + abs(move["sc"] - move["tc"]) == 1
        if move["shift"] and adjacent:
            direct = dict(move, shift=0, lr=move["sr"], lc=move["sc"])
            if apply_move(board, direct) == expected:
                move = direct
        submit(mirror, move, args.wechat)
        actual, last_frame = verify_move(mirror, samples, expected, move, args.wechat)
        if actual != expected:
            if last_frame is not None:
                args.debug.write_bytes(last_frame)
            if all(label == EMPTY for label in expected):
                print("Last move sent. Waiting for the result screen.", flush=True)
                return
            changes = [] if actual is None else [
                (i, board[i], expected[i], actual[i])
                for i in range(len(board)) if actual[i] != expected[i]
            ]
            raise RuntimeError(f"Move {number} {move}: board not confirmed: {changes[:30]}")
        board = expected
        print(f"Verified pair {number}/{len(moves)}", flush=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--limit", type=int)
    parser.add_argument("--horizontal", action="store_true")
    parser.add_argument("--wechat", action="store_true")
    parser.add_argument("--loop", action="store_true", help="auto-continue levels in Mac WeChat")
    parser.add_argument("--start-level", type=int)
    parser.add_argument("--stop-level", type=int)
    parser.add_argument("--debug", type=Path, default=LOCAL / "last-frame.png")
    args = parser.parse_args()
    if args.loop and (not args.wechat or not args.execute or args.limit is not None):
        parser.error("--loop requires --wechat --execute and cannot be combined with --limit")
    if args.stop_level is not None and (
        not args.loop or args.start_level is None or args.stop_level < args.start_level
    ):
        parser.error("--stop-level requires --loop and a valid --start-level")
    samples = load_samples("wechat-" if args.wechat else "")
    with mirror_session(args.wechat) as mirror:
        if args.loop:
            dialogs = load_dialogs()
            board = next_wechat_board(mirror, samples, dialogs, args.debug)
            for number in count(args.start_level or 1):
                name = "level" if args.start_level is not None else "board"
                print(f"Playing {name} {number}", flush=True)
                play_board(mirror, board, samples, args)
                if number == args.stop_level:
                    wait_for_reward(mirror, dialogs, args.debug)
                    print(f"Cleared level {number}; reward screen verified", flush=True)
                    return
                board = next_wechat_board(mirror, samples, dialogs, args.debug, after_clear=True)
        else:
            board, _ = confirm_board(mirror, samples, wechat=args.wechat)
            if board is None:
                raise RuntimeError("Mirror window produced no video frames")
            if None in board:
                raise RuntimeError(f"Unknown cells at {[i for i, label in enumerate(board) if label is None]}")
            play_board(mirror, board, samples, args)


if __name__ == "__main__":
    main()
