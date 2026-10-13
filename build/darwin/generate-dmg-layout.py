"""Regenerate dmg-layout.dsstore with ds-store==1.3.3 (packaging uses the asset)."""

from pathlib import Path

from ds_store import DSStore


with DSStore.open(str(Path(__file__).with_name("dmg-layout.dsstore")), "w+") as store:
    store["."]["vSrn"] = ("long", 1)
    store["."]["bwsp"] = {
        "WindowBounds": "{{180, 160}, {640, 320}}",
        "ShowToolbar": False,
        "ShowSidebar": False,
        "ShowStatusBar": False,
        "ShowPathbar": False,
        "ShowTabView": False,
        "ContainerShowSidebar": False,
        "PreviewPaneVisibility": False,
    }
    store["."]["icvp"] = {
        "viewOptionsVersion": 1,
        "backgroundType": 0,
        "arrangeBy": "none",
        "iconSize": 112.0,
        "textSize": 14.0,
        "labelOnBottom": True,
        "showIconPreview": False,
        "showItemInfo": False,
        "scrollPositionX": 0.0,
        "scrollPositionY": 0.0,
        "gridOffsetX": 0.0,
        "gridOffsetY": 0.0,
        "gridSpacing": 100.0,
    }
    store["."]["icvl"] = ("type", b"icnv")
    store["ChapterSmith.app"]["Iloc"] = (160, 140)
    store["Applications"]["Iloc"] = (480, 140)
