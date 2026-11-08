"""
Tests for resolve_driver.data_root() / ensure_data_dir(): the Python mirror of
Go's internal/datadir resolution, including the home-folder fallback and the
"data-root" marker. Hermetic: HOME is a tmp_path, the WS-01 constants are
monkeypatched to tmp_path, nothing touches /Volumes or the real ~/Library.
"""
import os
import sys
from unittest.mock import MagicMock


def _import_driver():
    sidecar_dir = os.path.dirname(os.path.abspath(__file__))
    if sidecar_dir not in sys.path:
        sys.path.insert(0, sidecar_dir)
    sys.modules.setdefault("DaVinciResolveScript", MagicMock())
    import resolve_driver
    return resolve_driver


rd = _import_driver()


def _env(monkeypatch, tmp_path, volume_mounted=False):
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.delenv(rd.DECKPLUS_DATA_ENV, raising=False)
    monkeypatch.delenv(rd.DECKPLUS_LEGACY_DATA_ENV, raising=False)
    vol = tmp_path / "Volumes" / "WS"
    default = str(vol / "2t5-deck-plus" / "data")
    legacy = str(vol / "ld-deck" / "data")
    monkeypatch.setattr(rd, "DECKPLUS_DEFAULT_DATA_ROOT", default)
    monkeypatch.setattr(rd, "DECKPLUS_LEGACY_DEFAULT_DATA_ROOT", legacy)
    if volume_mounted:
        os.makedirs(os.path.dirname(default))
    support = home / "Library" / "Application Support" / "2t5-deck-plus"
    return home, support, default, legacy


def _marker(support, body):
    support.mkdir(parents=True, exist_ok=True)
    (support / "data-root").write_text(body)


def test_env_wins(monkeypatch, tmp_path):
    _env(monkeypatch, tmp_path, volume_mounted=True)
    monkeypatch.setenv("DECKPLUS_DATA_DIR", "/tmp/x")
    assert rd.data_root() == "/tmp/x"
    monkeypatch.delenv("DECKPLUS_DATA_DIR")
    monkeypatch.setenv("LD_DECK_DATA_DIR", "/tmp/y")
    assert rd.data_root() == "/tmp/y"


def test_ws01_present_unchanged(monkeypatch, tmp_path):
    _, _, default, legacy = _env(monkeypatch, tmp_path)
    os.makedirs(legacy)
    assert rd.data_root() == legacy  # only the old folder exists
    os.makedirs(default)
    assert rd.data_root() == default  # new folder wins


def test_ws01_app_folder_only(monkeypatch, tmp_path):
    _, _, default, _ = _env(monkeypatch, tmp_path, volume_mounted=True)
    assert rd.data_root() == default


def test_no_volume_no_marker_uses_home(monkeypatch, tmp_path):
    home, support, _, _ = _env(monkeypatch, tmp_path)
    assert rd.data_root() == str(support / "data")
    assert not support.exists()  # data_root() writes nothing


def test_marker_external_without_volume_stays_external(monkeypatch, tmp_path):
    _, support, default, _ = _env(monkeypatch, tmp_path)
    _marker(support, "external\n" + default + "\n")
    assert rd.data_root() == default
    assert rd.ensure_data_dir("state") is None
    assert not os.path.exists(os.path.dirname(os.path.dirname(default)))  # nothing created


def test_marker_home_uses_home(monkeypatch, tmp_path):
    _, support, _, _ = _env(monkeypatch, tmp_path)
    _marker(support, "home\n" + str(support / "data") + "\n")
    assert rd.data_root() == str(support / "data")


def test_prior_install_without_marker_is_external(monkeypatch, tmp_path):
    _, support, default, _ = _env(monkeypatch, tmp_path)
    support.mkdir(parents=True)
    assert rd.data_root() == default
    assert rd.ensure_data_dir("state") is None


def test_ensure_data_dir_creates_home_tree(monkeypatch, tmp_path):
    _, support, _, _ = _env(monkeypatch, tmp_path)
    d = rd.ensure_data_dir("state")
    assert d == str(support / "data" / "state")
    assert os.path.isdir(d)
