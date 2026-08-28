from pathlib import Path

import mujoco

from fruit_ninja.simulation import CONTROL_HZ, PHYSICS_HZ, FruitNinjaSimulation


ROOT = Path(__file__).resolve().parents[1]


def test_scene_is_full_g1_with_dynamic_fruit_and_mocap_blade():
    model = mujoco.MjModel.from_xml_path(str(ROOT / "models/unitree_g1/fruit_ninja_scene.xml"))
    assert model.nu == 29
    assert model.nmocap == 1
    assert mujoco.mj_name2id(model, mujoco.mjtObj.mjOBJ_JOINT, "fruit_0_joint") >= 0
    assert mujoco.mj_name2id(model, mujoco.mjtObj.mjOBJ_GEOM, "blade_edge") >= 0


def test_demo_uses_real_contacts_and_produces_a_hit():
    simulation = FruitNinjaSimulation(enable_renderer=False)
    try:
        status = simulation.run_headless_steps(PHYSICS_HZ * 5)
        assert status["realPhysicsContacts"] is True
        assert status["launches"] >= 2
        assert status["hits"] >= 1
        assert status["error"] is None
    finally:
        simulation.close()


def test_timing_and_safety_contract():
    assert PHYSICS_HZ == 250
    assert CONTROL_HZ == 50
    source = (ROOT / "fruit_ninja/simulation.py").read_text(encoding="utf-8")
    assert "_target_has_collision" in source
    assert "np.clip(desired - self._guarded_target, -0.08, 0.08)" in source
    assert '"policyLoaded": False' in source
