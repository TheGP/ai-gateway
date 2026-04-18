#!/bin/bash

# Deploy script for ai-gateway
# 1. Pulls latest code from git
# 2. Checks if Go files changed
# 3. Recompiles if needed
# 4. Reloads or adds to pm2

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY_NAME="ai-gateway"
PM2_APP_NAME="ai-gateway"

echo "========================================"
echo "🚀 DEPLOYING AI GATEWAY"
echo "========================================"
cd "$SCRIPT_DIR"

# ── 1. Pull ───────────────────────────────────────────────────────────────────
BEFORE_HASH=$(git rev-parse HEAD)
echo "Pulling latest changes..."
git pull origin master
AFTER_HASH=$(git rev-parse HEAD)

if [ "$BEFORE_HASH" = "$AFTER_HASH" ]; then
    echo "✅ No changes detected."
    if ! command -v pm2 &> /dev/null; then
        echo "⚠️  pm2 not found — skipping process check."
        exit 0
    fi
    if pm2 describe "$PM2_APP_NAME" > /dev/null 2>&1; then
        STATUS=$(pm2 jlist 2>/dev/null | python3 -c "import sys,json; procs=json.load(sys.stdin); p=[x for x in procs if x['name']=='$PM2_APP_NAME']; print(p[0]['pm2_env']['status'] if p else 'unknown')" 2>/dev/null || echo "unknown")
        if [ "$STATUS" = "online" ]; then
            echo "✅ $PM2_APP_NAME is already running."
        else
            echo "⚠️  $PM2_APP_NAME is not running (status: $STATUS) — restarting..."
            pm2 restart "$PM2_APP_NAME"
            echo "✅ Restarted"
        fi
    else
        echo "⚠️  $PM2_APP_NAME not in pm2 — starting..."
        if [ ! -f "$SCRIPT_DIR/$BINARY_NAME" ]; then
            echo "Binary not found — building first..."
            go build -o "$BINARY_NAME" .
        fi
        pm2 start "./$BINARY_NAME" --name "$PM2_APP_NAME"
        pm2 save
        echo "✅ Started"
    fi
    pm2 status
    exit 0
fi

echo "Changes: $BEFORE_HASH → $AFTER_HASH"

# ── 2. Dependencies ───────────────────────────────────────────────────────────
echo ""
echo "========================================"
echo "STAGE 1: DEPENDENCIES"
echo "========================================"

MOD_CHANGES=$(git diff --name-only "$BEFORE_HASH" "$AFTER_HASH" -- "go.mod" "go.sum" || true)
if [ -n "$MOD_CHANGES" ]; then
    echo "go.mod/go.sum changed — downloading modules..."
    go mod download
    echo "✅ Dependencies updated"
else
    echo "✅ No module changes"
fi

# ── 3. Build ──────────────────────────────────────────────────────────────────
echo ""
echo "========================================"
echo "STAGE 2: BUILD"
echo "========================================"

GO_CHANGES=$(git diff --name-only "$BEFORE_HASH" "$AFTER_HASH" -- "*.go" "**/*.go" || true)

if [ ! -f "$SCRIPT_DIR/$BINARY_NAME" ]; then
    echo "⚠️  Binary not found — building for first time..."
    NEEDS_BUILD=1
elif [ -n "$GO_CHANGES" ]; then
    echo "Go files changed:"
    echo "$GO_CHANGES"
    NEEDS_BUILD=1
else
    echo "✅ No Go changes — skipping recompilation"
    NEEDS_BUILD=0
fi

if [ "${NEEDS_BUILD:-0}" = "1" ]; then
    echo "Building..."
    go build -o "$BINARY_NAME" .
    echo "✅ Build successful"
fi

# ── 4. PM2 ───────────────────────────────────────────────────────────────────
if ! command -v pm2 &> /dev/null; then
    echo "❌  pm2 not found. Build succeeded but process manager not started."
    echo "    Install it with:  npm install -g pm2"
    echo "    Then run:         pm2 startup   (to enable auto-start on reboot)"
    echo "    Then re-run:      bash deploy.sh"
    exit 1
fi

echo ""
echo "========================================"
echo "STAGE 3: PROCESS MANAGER"
echo "========================================"

if pm2 describe "$PM2_APP_NAME" > /dev/null 2>&1; then
    echo "Reloading $PM2_APP_NAME..."
    pm2 reload "$PM2_APP_NAME"
    echo "✅ Reloaded"
else
    echo "Starting $PM2_APP_NAME in pm2..."
    pm2 start "./$BINARY_NAME" --name "$PM2_APP_NAME"
    echo "✅ Started"
fi

pm2 save

echo ""
echo "========================================"
echo "🎉 DEPLOYMENT COMPLETE"
echo "========================================"
pm2 status
