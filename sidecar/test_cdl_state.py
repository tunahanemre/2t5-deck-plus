from cdl_state import NodeCDL

def test_neutral_default():
    n = NodeCDL()
    d = n.to_setcdl(node=1)
    assert d == {"NodeIndex": "1", "Slope": "1.0 1.0 1.0",
                 "Offset": "0.0 0.0 0.0", "Power": "1.0 1.0 1.0",
                 "Saturation": "1.0"}

def test_apply_offset_master():
    n = NodeCDL()
    n.apply("offset", "master", 0.1)   # master nudges all three channels
    assert n.offset == [0.1, 0.1, 0.1]

def test_apply_slope_blue():
    n = NodeCDL()
    n.apply("slope", "b", -0.2)
    assert n.slope == [1.0, 1.0, 0.8]

def test_sat_and_reset():
    n = NodeCDL()
    n.apply_sat(0.5)
    assert n.sat == 1.5
    n.reset()
    assert n.sat == 1.0 and n.slope == [1.0, 1.0, 1.0]

# reset_param tests

def test_reset_param_sat():
    n = NodeCDL()
    n.apply_sat(0.3)
    assert n.sat == 1.3
    n.reset_param("sat", "master")  # ch ignored for sat
    assert n.sat == 1.0

def test_reset_param_slope_master():
    n = NodeCDL()
    n.apply("slope", "master", 0.5)
    assert n.slope == [1.5, 1.5, 1.5]
    n.reset_param("slope", "master")
    assert n.slope == [1.0, 1.0, 1.0]

def test_reset_param_slope_channel():
    n = NodeCDL()
    n.apply("slope", "r", 0.3)
    n.apply("slope", "g", 0.2)
    n.reset_param("slope", "r")
    assert n.slope[0] == 1.0   # r reset
    assert n.slope[1] == 1.2   # g untouched

def test_reset_param_power_master():
    n = NodeCDL()
    n.apply("power", "master", -0.4)
    n.reset_param("power", "master")
    assert n.power == [1.0, 1.0, 1.0]

def test_reset_param_offset_master():
    n = NodeCDL()
    n.apply("offset", "master", 0.1)
    n.reset_param("offset", "master")
    assert n.offset == [0.0, 0.0, 0.0]

def test_reset_param_offset_channel():
    n = NodeCDL()
    n.apply("offset", "b", 0.25)
    n.apply("offset", "r", 0.1)
    n.reset_param("offset", "b")
    assert n.offset[2] == 0.0   # b reset
    assert n.offset[0] == 0.1   # r untouched
