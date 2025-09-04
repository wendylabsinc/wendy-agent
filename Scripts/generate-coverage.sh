#!/bin/bash
set -e

# Script to generate coverage reports locally
# Usage: ./Scripts/generate-coverage.sh [format]
# Formats: lcov (default), html, json

FORMAT="${1:-lcov}"
OS_TYPE=$(uname -s)

echo "Generating coverage report for $OS_TYPE in $FORMAT format..."

# Clean and build with tests
echo "Running tests with coverage enabled..."
swift test --enable-code-coverage

# Find the test binary based on OS
if [ "$OS_TYPE" = "Darwin" ]; then
    # macOS - look in both possible locations
    TEST_BINARY=$(find .build -name "*.xctest" -type d 2>/dev/null | grep -E "(debug|arm64-apple-macosx/debug)" | head -1)
    if [ -z "$TEST_BINARY" ]; then
        echo "Error: Could not find test binary"
        exit 1
    fi
    TEST_BINARY_NAME=$(basename "$TEST_BINARY" .xctest)
    TEST_BINARY_PATH="$TEST_BINARY/Contents/MacOS/$TEST_BINARY_NAME"
    COV_TOOL="xcrun llvm-cov"
    
    # Find profdata in the same directory structure
    PROF_DATA=$(find .build -name "default.profdata" -path "*/codecov/*" 2>/dev/null | head -1)
else
    # Linux
    TEST_BINARY=$(find .build/debug -name "*.xctest" -type f | head -1)
    if [ -z "$TEST_BINARY" ]; then
        echo "Error: Could not find test binary"
        exit 1
    fi
    TEST_BINARY_PATH="$TEST_BINARY"
    COV_TOOL="llvm-cov"
    PROF_DATA=".build/debug/codecov/default.profdata"
fi

# Check if profdata exists
if [ ! -f "$PROF_DATA" ]; then
    echo "Error: Profile data not found at $PROF_DATA"
    exit 1
fi

# Generate coverage based on format
case "$FORMAT" in
    lcov)
        echo "Generating LCOV report..."
        $COV_TOOL export \
            "$TEST_BINARY_PATH" \
            -instr-profile="$PROF_DATA" \
            --format=lcov > coverage.lcov
        echo "Coverage report generated: coverage.lcov"
        echo "Preview: $(head -20 coverage.lcov)"
        ;;
    
    html)
        echo "Generating HTML report..."
        $COV_TOOL show \
            "$TEST_BINARY_PATH" \
            -instr-profile="$PROF_DATA" \
            --format=html \
            -o coverage_report \
            Sources/
        echo "HTML coverage report generated in coverage_report/"
        if [ "$OS_TYPE" = "Darwin" ]; then
            echo "Opening report in browser..."
            open coverage_report/index.html
        else
            echo "Open coverage_report/index.html in your browser to view the report"
        fi
        ;;
    
    json)
        echo "Generating JSON report..."
        $COV_TOOL export \
            "$TEST_BINARY_PATH" \
            -instr-profile="$PROF_DATA" \
            --format=json > coverage.json
        echo "Coverage report generated: coverage.json"
        ;;
    
    summary)
        echo "Coverage Summary:"
        $COV_TOOL report \
            "$TEST_BINARY_PATH" \
            -instr-profile="$PROF_DATA" \
            Sources/
        ;;
    
    *)
        echo "Unknown format: $FORMAT"
        echo "Supported formats: lcov, html, json, summary"
        exit 1
        ;;
esac

echo "Done!"