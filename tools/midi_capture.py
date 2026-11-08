#!/usr/bin/env python3
"""Listen to the Loupedeck+ MIDI port and print every message.
Turn each knob one click each way and press each button; record the output.
Quit the Logi/Loupedeck app first (it holds the port)."""
import sys, time
try:
    import mido  # pip install mido python-rtmidi
except ImportError:
    sys.exit("pip install mido python-rtmidi first")

ports = [p for p in mido.get_input_names() if "loupedeck" in p.lower()]
print("MIDI inputs:", mido.get_input_names())
if not ports:
    sys.exit("No Loupedeck port found — is the Logi app still running?")
print("Listening on:", ports[0], "— operate each control, Ctrl-C when done")
with mido.open_input(ports[0]) as inp:
    for msg in inp:
        print(time.strftime("%H:%M:%S"), msg)
