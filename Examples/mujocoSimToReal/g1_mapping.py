"""Unitree G1 29-DOF MuJoCo-to-DDS joint mapping.

The DDS indices match /Users/smile/dog/models/unitree_g1_coffee/g1_joint_index_dds.md.
Limits are copied from the local g1_29dof.xml and are used for simulation-side
clamping before targets are streamed to the robot.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class G1Joint:
    mujoco: str
    dds_index: int
    lower: float
    upper: float
    group: str


G1_JOINTS: tuple[G1Joint, ...] = (
    G1Joint("left_hip_pitch_joint", 0, -2.5307, 2.8798, "left_leg"),
    G1Joint("left_hip_roll_joint", 1, -0.5236, 2.9671, "left_leg"),
    G1Joint("left_hip_yaw_joint", 2, -2.7576, 2.7576, "left_leg"),
    G1Joint("left_knee_joint", 3, -0.087267, 2.8798, "left_leg"),
    G1Joint("left_ankle_pitch_joint", 4, -0.87267, 0.5236, "left_leg"),
    G1Joint("left_ankle_roll_joint", 5, -0.2618, 0.2618, "left_leg"),
    G1Joint("right_hip_pitch_joint", 6, -2.5307, 2.8798, "right_leg"),
    G1Joint("right_hip_roll_joint", 7, -2.9671, 0.5236, "right_leg"),
    G1Joint("right_hip_yaw_joint", 8, -2.7576, 2.7576, "right_leg"),
    G1Joint("right_knee_joint", 9, -0.087267, 2.8798, "right_leg"),
    G1Joint("right_ankle_pitch_joint", 10, -0.87267, 0.5236, "right_leg"),
    G1Joint("right_ankle_roll_joint", 11, -0.2618, 0.2618, "right_leg"),
    G1Joint("waist_yaw_joint", 12, -2.618, 2.618, "waist"),
    G1Joint("waist_roll_joint", 13, -0.52, 0.52, "waist"),
    G1Joint("waist_pitch_joint", 14, -0.52, 0.52, "waist"),
    G1Joint("left_shoulder_pitch_joint", 15, -3.0892, 2.6704, "left_arm"),
    G1Joint("left_shoulder_roll_joint", 16, -1.5882, 2.2515, "left_arm"),
    G1Joint("left_shoulder_yaw_joint", 17, -2.618, 2.618, "left_arm"),
    G1Joint("left_elbow_joint", 18, -1.0472, 2.0944, "left_arm"),
    G1Joint("left_wrist_roll_joint", 19, -1.97222, 1.97222, "left_arm"),
    G1Joint("left_wrist_pitch_joint", 20, -1.61443, 1.61443, "left_arm"),
    G1Joint("left_wrist_yaw_joint", 21, -1.61443, 1.61443, "left_arm"),
    G1Joint("right_shoulder_pitch_joint", 22, -3.0892, 2.6704, "right_arm"),
    G1Joint("right_shoulder_roll_joint", 23, -2.2515, 1.5882, "right_arm"),
    G1Joint("right_shoulder_yaw_joint", 24, -2.618, 2.618, "right_arm"),
    G1Joint("right_elbow_joint", 25, -1.0472, 2.0944, "right_arm"),
    G1Joint("right_wrist_roll_joint", 26, -1.97222, 1.97222, "right_arm"),
    G1Joint("right_wrist_pitch_joint", 27, -1.61443, 1.61443, "right_arm"),
    G1Joint("right_wrist_yaw_joint", 28, -1.61443, 1.61443, "right_arm"),
)

JOINT_BY_NAME = {joint.mujoco: joint for joint in G1_JOINTS}
JOINT_BY_DDS = {joint.dds_index: joint for joint in G1_JOINTS}

GROUPS: dict[str, tuple[str, ...]] = {
    "left_arm": tuple(j.mujoco for j in G1_JOINTS if j.group == "left_arm"),
    "right_arm": tuple(j.mujoco for j in G1_JOINTS if j.group == "right_arm"),
    "arms": tuple(j.mujoco for j in G1_JOINTS if j.group in {"left_arm", "right_arm"}),
    "left_leg": tuple(j.mujoco for j in G1_JOINTS if j.group == "left_leg"),
    "right_leg": tuple(j.mujoco for j in G1_JOINTS if j.group == "right_leg"),
    "legs": tuple(j.mujoco for j in G1_JOINTS if j.group in {"left_leg", "right_leg"}),
    "waist": tuple(j.mujoco for j in G1_JOINTS if j.group == "waist"),
    "all": tuple(j.mujoco for j in G1_JOINTS),
}


def clamp_joint(name: str, value: float) -> float:
    joint = JOINT_BY_NAME[name]
    return min(max(value, joint.lower), joint.upper)


def names_for_group(group: str) -> tuple[str, ...]:
    try:
        return GROUPS[group]
    except KeyError as exc:
        valid = ", ".join(sorted(GROUPS))
        raise ValueError(f"unknown group {group!r}; valid groups: {valid}") from exc
