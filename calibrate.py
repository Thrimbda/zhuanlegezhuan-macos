"""Build private visual templates from a local game screenshot."""
import argparse
from collections import Counter
import json
from pathlib import Path

from PIL import Image

from play_mirror import COLS, DIALOGS, EMPTY, LOCAL, ROWS, WECHAT_SIZE, patches


def save_tiles(image, labels_path, wechat):
    grid = [line.split() for line in labels_path.read_text().splitlines() if line.strip()]
    if len(grid) != ROWS or any(len(row) != COLS for row in grid):
        raise ValueError(f"Labels must have {ROWS} rows of {COLS} space-separated tokens.")
    tokens = [token for row in grid for token in row]
    counts = Counter(tokens)
    if "." not in counts or len(counts) == 1 or any(n % 2 for n in counts.values()):
        raise ValueError("Include empty cells (.) and use an even count for every tile label.")
    ids = {token: i for i, token in enumerate(sorted(counts.keys() - {"."}))}
    labels = [EMPTY if token == "." else ids[token] for token in tokens]
    tiles = patches(image, wechat)
    atlas = Image.new("RGB", (18 * COLS, 18 * ROWS))
    for i, tile in enumerate(tiles):
        row, col = divmod(i, COLS)
        atlas.paste(tile, (col * 18, row * 18))
    prefix = "wechat-" if wechat else ""
    atlas.save(LOCAL / f"{prefix}tile-samples.png")
    (LOCAL / f"{prefix}tile-labels.json").write_text(json.dumps({"board": labels}) + "\n")


def save_reward(image):
    if image.size != WECHAT_SIZE:
        raise ValueError(f"Expected a Mac WeChat window screenshot of {WECHAT_SIZE}.")
    image = image.convert("RGB")
    for name, box in DIALOGS["reward"]:
        image.crop(box).save(LOCAL / name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="mode", required=True)
    tiles = commands.add_parser("tiles", help="calibrate tile recognition")
    tiles.add_argument("--wechat", action="store_true")
    tiles.add_argument("screenshot", type=Path)
    tiles.add_argument("labels", type=Path)
    reward = commands.add_parser("reward", help="calibrate the Mac WeChat reward screen")
    reward.add_argument("screenshot", type=Path)
    args = parser.parse_args()

    LOCAL.mkdir(exist_ok=True)
    with Image.open(args.screenshot) as image:
        try:
            if args.mode == "tiles":
                save_tiles(image, args.labels, args.wechat)
            else:
                save_reward(image)
        except (OSError, ValueError, RuntimeError) as error:
            parser.error(str(error))
    print(f"Templates saved in {LOCAL}")


if __name__ == "__main__":
    main()
