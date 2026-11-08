CH = {"r": 0, "g": 1, "b": 2}

# Neutral values for each CDL parameter.
_NEUTRAL = {"slope": 1.0, "power": 1.0, "offset": 0.0}

class NodeCDL:
    def __init__(self):
        self.slope = [1.0, 1.0, 1.0]
        self.offset = [0.0, 0.0, 0.0]
        self.power = [1.0, 1.0, 1.0]
        self.sat = 1.0

    def apply(self, param, ch, amt):
        arr = {"slope": self.slope, "offset": self.offset, "power": self.power}[param]
        if ch == "master":
            for i in range(3):
                arr[i] = round(arr[i] + amt, 4)
        else:
            arr[CH[ch]] = round(arr[CH[ch]] + amt, 4)

    def apply_sat(self, amt):
        self.sat = round(self.sat + amt, 4)

    def reset_param(self, param, ch):
        """Reset a single parameter (or just sat) back to its neutral value.

        param='sat'          -> sat = 1.0 (ch is ignored)
        param='slope'|'power', ch='master'  -> all three channels -> 1.0
        param='slope'|'power', ch='r'|'g'|'b' -> that channel -> 1.0
        param='offset', ch='master'         -> all three channels -> 0.0
        param='offset', ch='r'|'g'|'b'     -> that channel -> 0.0
        """
        if param == "sat":
            self.sat = 1.0
            return
        neutral = _NEUTRAL[param]
        arr = {"slope": self.slope, "offset": self.offset, "power": self.power}[param]
        if ch == "master":
            for i in range(3):
                arr[i] = neutral
        else:
            arr[CH[ch]] = neutral

    def reset(self):
        self.__init__()

    def to_setcdl(self, node):
        f3 = lambda a: " ".join(str(x) for x in a)
        return {"NodeIndex": str(node), "Slope": f3(self.slope),
                "Offset": f3(self.offset), "Power": f3(self.power),
                "Saturation": str(self.sat)}
