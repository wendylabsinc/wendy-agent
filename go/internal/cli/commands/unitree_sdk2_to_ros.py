#!/usr/bin/env python3
"""Generate ROS .msg files from Unitree SDK2's generated DDS headers.

The public unitree_ros2 repository can lag the interfaces shipped in SDK2 and
on robot firmware. Reading field declarations from a pinned SDK2 checkout keeps
Wendy from maintaining a handwritten copy of those layouts. Unknown C++ field
types are rejected instead of producing a potentially unsafe schema.
"""

import argparse
import re
from pathlib import Path


SCHEMAS = (
    ("go2/ConfigChangeStatus_.hpp", "unitree_go", "ConfigChangeStatus", True),
    ("hg/AgvBmsState_.hpp", "unitree_hg", "AgvBmsState", True),
    ("hg/SportModeState_.hpp", "unitree_hg", "SportModeState", True),
    # Some G1 firmware advertises SymState, but current public SDK2 releases do
    # not ship its definition. Generate it automatically if Unitree adds it to
    # a future pinned SDK revision; never guess its layout.
    ("go2/SymState_.hpp", "unitree_go", "SymState", False),
)

PRIMITIVES = {
    "bool": "bool",
    "float": "float32",
    "double": "float64",
    "int8_t": "int8",
    "uint8_t": "uint8",
    "int16_t": "int16",
    "uint16_t": "uint16",
    "int32_t": "int32",
    "uint32_t": "uint32",
    "int64_t": "int64",
    "uint64_t": "uint64",
    "std::string": "string",
}


def ros_type(cpp_type: str) -> str:
    cpp_type = " ".join(cpp_type.split())
    if cpp_type in PRIMITIVES:
        return PRIMITIVES[cpp_type]

    array = re.fullmatch(r"std::array<\s*(.+?)\s*,\s*(\d+)\s*>", cpp_type)
    if array:
        return f"{ros_type(array.group(1))}[{array.group(2)}]"

    vector = re.fullmatch(r"std::vector<\s*(.+?)\s*>", cpp_type)
    if vector:
        return f"{ros_type(vector.group(1))}[]"

    raise ValueError(f"unsupported Unitree SDK2 field type: {cpp_type}")


def fields_from_header(header: Path, message: str) -> list[tuple[str, str]]:
    source = header.read_text(encoding="utf-8")
    match = re.search(
        rf"class\s+{re.escape(message)}_\s*\{{\s*private:(.*?)\bpublic:",
        source,
        re.DOTALL,
    )
    if not match:
        raise ValueError(f"could not locate private fields for {message} in {header}")

    fields = []
    declaration = re.compile(
        r"^\s*(.+?)\s+([A-Za-z][A-Za-z0-9_]*)_\s*(?:=.*)?;\s*$"
    )
    for line in match.group(1).splitlines():
        line = line.strip()
        if not line:
            continue
        field = declaration.fullmatch(line)
        if not field:
            raise ValueError(f"unsupported declaration in {header}: {line}")
        fields.append((ros_type(field.group(1)), field.group(2)))

    if not fields:
        raise ValueError(f"no fields found for {message} in {header}")
    return fields


def install_schema(
    sdk_root: Path,
    ros_root: Path,
    source_commit: str,
    relative_header: str,
    package: str,
    message: str,
    required: bool,
) -> None:
    header = sdk_root / "include" / "unitree" / "idl" / relative_header
    if not header.exists():
        if required:
            raise FileNotFoundError(f"required SDK2 schema is missing: {header}")
        print(f"optional SDK2 schema is not public yet; skipping {message}")
        return

    fields = fields_from_header(header, message)
    package_root = ros_root / package
    message_path = package_root / "msg" / f"{message}.msg"
    message_path.write_text(
        "# Generated from unitreerobotics/unitree_sdk2 at "
        f"{source_commit}; do not edit.\n"
        + "".join(f"{field_type} {name}\n" for field_type, name in fields),
        encoding="utf-8",
    )

    cmake_path = package_root / "CMakeLists.txt"
    cmake = cmake_path.read_text(encoding="utf-8")
    schema_entry = f'        "msg/{message}.msg"'
    if schema_entry not in cmake:
        marker = "rosidl_generate_interfaces(${PROJECT_NAME}\n"
        if marker not in cmake:
            raise ValueError(f"could not locate rosidl interface list in {cmake_path}")
        cmake = cmake.replace(marker, marker + schema_entry + "\n", 1)
        cmake_path.write_text(cmake, encoding="utf-8")

    print(f"generated {package}/msg/{message} from {relative_header}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-root", type=Path, required=True)
    parser.add_argument("--ros-root", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    args = parser.parse_args()

    for schema in SCHEMAS:
        install_schema(args.sdk_root, args.ros_root, args.source_commit, *schema)


if __name__ == "__main__":
    main()
