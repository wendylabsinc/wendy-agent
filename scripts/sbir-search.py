#!/usr/bin/env python3
"""
SBIR/STTR Opportunity Finder for Wendy Labs Inc.

Searches sbir.gov API for open solicitations matching Wendy Labs' capabilities:
- Edge computing / Edge AI
- Robotics / Autonomous systems
- IoT / Sensor networks
- Computer vision / AI inference
- Worker safety / Hazardous environment monitoring
- Fleet management / Device management
- Drone / UAV systems
- Industrial inspection
- Secure communications / PKI / Zero-trust

Usage:
    python3 sbir-search.py [--days 7] [--max-results 50]
"""

import json
import sys
import urllib.request
import urllib.parse
import urllib.error
from datetime import datetime, timedelta
from typing import Any

SBIR_API_BASE = "https://www.sbir.gov/api/solicitations.json"

# Keywords mapped to Wendy Labs capabilities
SEARCH_KEYWORDS = [
    "edge computing",
    "edge AI",
    "autonomous systems",
    "robotics platform",
    "robot inspection",
    "IoT platform",
    "sensor network",
    "computer vision edge",
    "AI inference edge",
    "worker safety",
    "hazardous environment monitoring",
    "fleet management",
    "device management",
    "drone UAV",
    "unmanned systems",
    "industrial inspection",
    "secure device",
    "zero trust IoT",
    "embedded AI",
    "real-time AI",
    "autonomous vehicle",
    "NVIDIA Jetson",
    "edge device",
    "operational technology",
    "SCADA monitoring",
    "gas detection",
    "leak detection",
    "physical AI",
    "ROS robotics",
    "container orchestration edge",
    "OTA update",
    "embedded Linux",
]

# Agencies most relevant to Wendy Labs
TARGET_AGENCIES = [
    "DOD",   # Department of Defense
    "DOE",   # Department of Energy
    "DHS",   # Department of Homeland Security
    "NASA",  # NASA
    "NSF",   # National Science Foundation
    "DARPA", # DARPA (under DOD)
    "NAVY",  # Navy
    "ARMY",  # Army
    "AF",    # Air Force
    "EPA",   # Environmental Protection Agency
    "DOT",   # Department of Transportation
    "USDA",  # Agriculture (for environmental sensors)
]


def fetch_open_solicitations(keyword: str = "") -> list[dict[str, Any]]:
    """Fetch open SBIR/STTR solicitations from sbir.gov API."""
    params = {
        "keyword": keyword,
        "open": "1",  # Only open solicitations
    }
    url = f"{SBIR_API_BASE}?{urllib.parse.urlencode(params)}"

    try:
        req = urllib.request.Request(url, headers={"User-Agent": "WendyLabs-SBIR-Search/1.0"})
        with urllib.request.urlopen(req, timeout=30) as response:
            data = json.loads(response.read().decode())
            if isinstance(data, list):
                return data
            return []
    except (urllib.error.URLError, json.JSONDecodeError, TimeoutError) as e:
        print(f"  Warning: Failed to fetch for keyword '{keyword}': {e}", file=sys.stderr)
        return []


def score_opportunity(solicitation: dict[str, Any]) -> int:
    """Score how relevant a solicitation is to Wendy Labs (0-100)."""
    score = 0
    text = " ".join([
        solicitation.get("solicitation_title", ""),
        solicitation.get("solicitation_topics", ""),
        str(solicitation.get("sbir_solicitation_topic", "")),
    ]).lower()

    # High-value keywords (core to Wendy's business)
    high_value = [
        "edge computing", "edge ai", "embedded ai", "embedded linux",
        "robotics", "autonomous", "robot inspection",
        "iot platform", "iot device", "device management",
        "computer vision", "ai inference", "real-time ai",
        "nvidia", "jetson", "gpu edge",
        "worker safety", "hazardous", "gas detection",
        "fleet management", "over-the-air", "ota",
        "physical ai", "ros", "ros2", "ros 2",
    ]
    for kw in high_value:
        if kw in text:
            score += 15

    # Medium-value keywords
    medium_value = [
        "drone", "uav", "unmanned", "sensor network",
        "container", "docker", "linux",
        "secure communication", "pki", "zero trust", "mTLS",
        "industrial inspection", "pipeline inspection",
        "leak detection", "anomaly detection",
        "scada", "operational technology",
        "machine learning edge", "deep learning edge",
    ]
    for kw in medium_value:
        if kw in text:
            score += 8

    # Lower-value but still relevant
    low_value = [
        "artificial intelligence", "machine learning",
        "cybersecurity", "critical infrastructure",
        "energy", "oil and gas", "pipeline",
        "environmental monitoring", "sensor",
        "raspberry pi", "arm", "aarch64",
        "yocto", "embedded system",
    ]
    for kw in low_value:
        if kw in text:
            score += 4

    return min(score, 100)


def format_date(date_str: str | None) -> str:
    """Format date string for display."""
    if not date_str:
        return "N/A"
    try:
        dt = datetime.strptime(date_str[:10], "%Y-%m-%d")
        return dt.strftime("%B %d, %Y")
    except (ValueError, IndexError):
        return date_str or "N/A"


def days_until(date_str: str | None) -> int | None:
    """Calculate days until a deadline."""
    if not date_str:
        return None
    try:
        dt = datetime.strptime(date_str[:10], "%Y-%m-%d")
        delta = dt - datetime.now()
        return delta.days
    except (ValueError, IndexError):
        return None


def main():
    import argparse
    parser = argparse.ArgumentParser(description="Find SBIR/STTR opportunities for Wendy Labs")
    parser.add_argument("--max-results", type=int, default=25, help="Max results to show")
    parser.add_argument("--min-score", type=int, default=8, help="Minimum relevance score")
    parser.add_argument("--json", action="store_true", help="Output as JSON")
    args = parser.parse_args()

    print("🔍 Searching SBIR/STTR opportunities for Wendy Labs...\n")

    # Deduplicate by solicitation number
    seen: set[str] = set()
    all_results: list[dict[str, Any]] = []

    for keyword in SEARCH_KEYWORDS:
        results = fetch_open_solicitations(keyword)
        for r in results:
            sol_id = r.get("solicitation_number", r.get("solicitation_title", ""))
            if sol_id not in seen:
                seen.add(sol_id)
                relevance = score_opportunity(r)
                if relevance >= args.min_score:
                    r["_relevance_score"] = relevance
                    r["_matched_keyword"] = keyword
                    all_results.append(r)

    # Sort by relevance score descending
    all_results.sort(key=lambda x: x.get("_relevance_score", 0), reverse=True)
    all_results = all_results[:args.max_results]

    if args.json:
        print(json.dumps(all_results, indent=2, default=str))
        return

    if not all_results:
        print("No matching open SBIR/STTR solicitations found.")
        return

    print(f"Found {len(all_results)} relevant open SBIR/STTR opportunities:\n")
    print("=" * 80)

    for i, sol in enumerate(all_results, 1):
        title = sol.get("solicitation_title", "Untitled")
        agency = sol.get("agency", "Unknown")
        branch = sol.get("branch", "")
        program = sol.get("program", "SBIR")
        phase = sol.get("phase", "")
        sol_number = sol.get("solicitation_number", "N/A")
        open_date = format_date(sol.get("open_date"))
        close_date = format_date(sol.get("close_date"))
        days_left = days_until(sol.get("close_date"))
        score = sol.get("_relevance_score", 0)
        url = sol.get("solicitation_url") or sol.get("url", "")

        urgency = ""
        if days_left is not None:
            if days_left < 0:
                urgency = "⚠️  CLOSED"
            elif days_left <= 7:
                urgency = f"🔴 {days_left} days left!"
            elif days_left <= 30:
                urgency = f"🟡 {days_left} days left"
            else:
                urgency = f"🟢 {days_left} days left"

        print(f"\n#{i}  [{program} {phase}] {title}")
        print(f"    Agency: {agency} {branch}")
        print(f"    Solicitation #: {sol_number}")
        print(f"    Relevance Score: {'⭐' * min(score // 10, 5)} ({score}/100)")
        print(f"    Open: {open_date}  |  Close: {close_date}  {urgency}")
        if url:
            print(f"    URL: {url}")
        print("-" * 80)

    print(f"\n✅ Total: {len(all_results)} opportunities found")
    print("💡 Review these at https://www.sbir.gov/sbirsearch/topic/current")


if __name__ == "__main__":
    main()
