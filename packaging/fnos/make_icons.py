"""Draw the fnOS application icons.

Rendered at 1024px and downsampled, so the small sizes stay legible.
"""

from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parent

BG = (15, 23, 42, 255)  # slate-900
EDGE = (51, 65, 85, 255)  # slate-700
BODY = (226, 232, 240, 255)  # slate-200
LENS = (56, 189, 248, 255)  # sky-400
DARK = (2, 6, 23, 255)  # slate-950
RED = (248, 113, 113, 255)  # red-400


def render(size: int = 1024) -> Image.Image:
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    pad = size * 0.06
    d.rounded_rectangle(
        (pad, pad, size - pad, size - pad),
        radius=size * 0.22,
        fill=BG,
        outline=EDGE,
        width=max(1, size // 128),
    )

    # Camera body.
    left, top = size * 0.17, size * 0.30
    right, bottom = size * 0.83, size * 0.74
    d.rounded_rectangle(
        (left, top, right, bottom),
        radius=size * 0.07,
        fill=BODY,
    )

    # Viewfinder bump.
    d.rounded_rectangle(
        (size * 0.30, size * 0.22, size * 0.48, size * 0.32),
        radius=size * 0.03,
        fill=BODY,
    )

    # Lens: accent ring around a dark pupil.
    cx, cy = size * 0.50, size * 0.52
    r = size * 0.135
    d.ellipse((cx - r, cy - r, cx + r, cy + r), fill=LENS)
    r2 = size * 0.085
    d.ellipse((cx - r2, cy - r2, cx + r2, cy + r2), fill=DARK)

    # Recording dot.
    d.ellipse(
        (size * 0.72, size * 0.36, size * 0.78, size * 0.42),
        fill=RED,
    )

    return img


def main() -> None:
    master = render(1024)

    targets = [
        (ROOT / "ICON.PNG", 128),
        (ROOT / "ICON_256.PNG", 256),
        (ROOT / "app/ui/images/icon_64.png", 64),
        (ROOT / "app/ui/images/icon_256.png", 256),
    ]

    for path, size in targets:
        path.parent.mkdir(parents=True, exist_ok=True)
        master.resize((size, size), Image.LANCZOS).save(path, "PNG")
        print(f"{path.relative_to(ROOT)}  {size}x{size}")


if __name__ == "__main__":
    main()
