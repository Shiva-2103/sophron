"""Render docs diagrams (diagrams.html) to PNG images in docs/assets/.

Needs the Poppins font installed on your machine, plus:
    pip install playwright && playwright install chromium
Run from the repo root:
    python3 docs/assets/src/render.py
"""
from pathlib import Path
from playwright.sync_api import sync_playwright

SRC = Path(__file__).parent / "diagrams.html"
OUT = Path(__file__).parent.parent
DIAGRAMS = ["overview", "architecture", "fix-flow", "roadmap"]

with sync_playwright() as p:
    browser = p.chromium.launch()
    page = browser.new_page(viewport={"width": 1100, "height": 900}, device_scale_factor=2)
    page.goto(SRC.resolve().as_uri())
    page.evaluate("document.fonts.ready")
    for name in DIAGRAMS:
        page.locator(f"#{name}").screenshot(path=str(OUT / f"{name}.png"), omit_background=True)
        print(f"wrote {name}.png")
    browser.close()
