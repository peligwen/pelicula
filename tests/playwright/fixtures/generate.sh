#!/usr/bin/env bash
# Generate Playwright test fixture files.
# Usage: bash tests/playwright/fixtures/generate.sh <output_dir>
# Requires: ffmpeg on PATH (or will attempt to use procula container)
set -euo pipefail

OUT="${1:?Usage: generate.sh <output_dir>}"
mkdir -p "$OUT"

# Helper: run ffmpeg, fall back to procula container
run_ffmpeg() {
    if command -v ffmpeg &>/dev/null; then
        ffmpeg "$@"
    else
        # Try inside the procula container (has ffmpeg installed)
        docker exec pelicula-test-procula ffmpeg "$@"
    fi
}

echo "Generating fixtures in $OUT..."

# 1. valid-h264-10s.mkv — Standard H.264/AAC, 10s, 320x240
run_ffmpeg -y \
    -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
    -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
    -c:v libx264 -preset ultrafast -crf 28 \
    -c:a aac -b:a 64k \
    "$OUT/valid-h264-10s.mkv" 2>/dev/null
echo "  ✓ valid-h264-10s.mkv"

# 2. valid-h265-10s.mkv — H.265/AAC, 10s, 320x240
run_ffmpeg -y \
    -f lavfi -i "color=c=green:s=320x240:d=10:r=24" \
    -f lavfi -i "sine=frequency=880:duration=10:sample_rate=44100" \
    -c:v libx265 -preset ultrafast -crf 28 \
    -c:a aac -b:a 64k \
    "$OUT/valid-h265-10s.mkv" 2>/dev/null
echo "  ✓ valid-h265-10s.mkv"

# 3. no-audio.mkv — Video only, no audio track
run_ffmpeg -y \
    -f lavfi -i "color=c=red:s=320x240:d=10:r=24" \
    -c:v libx264 -preset ultrafast -crf 28 \
    "$OUT/no-audio.mkv" 2>/dev/null
echo "  ✓ no-audio.mkv"

# 4. Night.of.the.Living.Dead.1968.mkv — Public domain title for subtitle acquisition
#    Metadata matches the real film so subtitle providers can identify it.
run_ffmpeg -y \
    -f lavfi -i "color=c=black:s=320x240:d=15:r=24" \
    -f lavfi -i "sine=frequency=220:duration=15:sample_rate=44100" \
    -c:v libx264 -preset ultrafast -crf 28 \
    -c:a aac -b:a 64k \
    -metadata title="Night of the Living Dead" \
    -metadata year="1968" \
    -metadata comment="Pelicula test fixture — not the real film" \
    "$OUT/Night.of.the.Living.Dead.1968.mkv" 2>/dev/null
echo "  ✓ Night.of.the.Living.Dead.1968.mkv"

# 5. corrupt-header.mkv — Truncated file (simulates corrupt download)
#    Generate a valid file then truncate it to 512 bytes.
run_ffmpeg -y \
    -f lavfi -i "color=c=white:s=320x240:d=10:r=24" \
    -f lavfi -i "sine=frequency=1000:duration=10:sample_rate=44100" \
    -c:v libx264 -preset ultrafast -crf 28 \
    -c:a aac -b:a 64k \
    -f matroska "$OUT/corrupt-header-full.mkv" 2>/dev/null
dd if="$OUT/corrupt-header-full.mkv" of="$OUT/corrupt-header.mkv" bs=512 count=1 2>/dev/null
rm -f "$OUT/corrupt-header-full.mkv"
echo "  ✓ corrupt-header.mkv"

# 6. sample-file.mkv — A valid but very small file (simulates scene sample/fake release)
#    Kept at ≤100KB so it triggers the sample-detection floor in Procula.
run_ffmpeg -y \
    -f lavfi -i "color=c=yellow:s=160x120:d=2:r=12" \
    -f lavfi -i "sine=frequency=500:duration=2:sample_rate=22050" \
    -c:v libx264 -preset ultrafast -crf 51 \
    -c:a aac -b:a 32k \
    "$OUT/sample-file.mkv" 2>/dev/null
echo "  ✓ sample-file.mkv"

echo ""
echo "Done. Fixtures written to $OUT"
