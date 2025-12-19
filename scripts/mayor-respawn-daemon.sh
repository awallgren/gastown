#!/bin/bash
# Mayor Respawn Daemon
# Watches for restart requests and respawns the mayor session
#
# Usage: mayor-respawn-daemon.sh [start|stop|status]
#
# The daemon monitors for mail to "daemon/" with subject containing "RESTART".
# When found, it:
#   1. Acknowledges the mail
#   2. Waits 5 seconds (for handoff mail to be sent)
#   3. Runs `gt mayor restart`

DAEMON_NAME="gt-mayor-respawn"
PID_FILE="/tmp/${DAEMON_NAME}.pid"
LOG_FILE="/tmp/${DAEMON_NAME}.log"
CHECK_INTERVAL=10  # seconds between mail checks
TOWN_ROOT="${GT_TOWN_ROOT:-/Users/stevey/gt}"

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG_FILE"
}

check_for_restart() {
    cd "$TOWN_ROOT" || return 1

    # Check inbox for daemon identity - look for RESTART subject
    # Set BD_IDENTITY=daemon so bd mail knows which inbox to check
    local inbox
    inbox=$(BD_IDENTITY=daemon bd mail inbox --json 2>/dev/null)

    if [ -z "$inbox" ] || [ "$inbox" = "null" ] || [ "$inbox" = "[]" ]; then
        return 1
    fi

    # Parse JSON to find RESTART messages
    # Note: bd mail returns "title" not "subject" (beads uses title for message subjects)
    local msg_id
    msg_id=$(echo "$inbox" | jq -r '.[] | select(.title | test("RESTART"; "i")) | .id' 2>/dev/null | head -1)

    if [ -n "$msg_id" ] && [ "$msg_id" != "null" ]; then
        log "Found restart request: $msg_id"

        # Acknowledge the message
        BD_IDENTITY=daemon bd mail ack "$msg_id" 2>/dev/null
        log "Acknowledged restart request"

        # Wait for handoff to complete
        sleep 5

        # Restart mayor (just sends Ctrl-C, loop handles respawn)
        log "Triggering mayor respawn..."
        gt mayor restart 2>&1 | while read -r line; do log "$line"; done
        log "Mayor respawn triggered"

        return 0
    fi

    return 1
}

daemon_loop() {
    log "Daemon starting, watching for restart requests..."

    while true; do
        if check_for_restart; then
            log "Restart handled, continuing watch..."
        fi
        sleep "$CHECK_INTERVAL"
    done
}

start_daemon() {
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE")
        if kill -0 "$pid" 2>/dev/null; then
            echo "Daemon already running (PID $pid)"
            return 1
        fi
        rm -f "$PID_FILE"
    fi

    # Start daemon in background using the script itself
    nohup "$0" run > /dev/null 2>&1 &

    local pid=$!
    echo "$pid" > "$PID_FILE"
    echo "Started mayor respawn daemon (PID $pid)"
    echo "Log: $LOG_FILE"
}

run_daemon() {
    # Called when script is invoked with "run"
    echo $$ > "$PID_FILE"
    daemon_loop
}

stop_daemon() {
    if [ ! -f "$PID_FILE" ]; then
        echo "Daemon not running (no PID file)"
        return 1
    fi

    local pid
    pid=$(cat "$PID_FILE")

    if kill -0 "$pid" 2>/dev/null; then
        kill "$pid"
        rm -f "$PID_FILE"
        echo "Stopped daemon (PID $pid)"
    else
        rm -f "$PID_FILE"
        echo "Daemon was not running (stale PID file removed)"
    fi
}

daemon_status() {
    if [ ! -f "$PID_FILE" ]; then
        echo "Daemon not running"
        return 1
    fi

    local pid
    pid=$(cat "$PID_FILE")

    if kill -0 "$pid" 2>/dev/null; then
        echo "Daemon running (PID $pid)"
        echo "Log: $LOG_FILE"
        if [ -f "$LOG_FILE" ]; then
            echo ""
            echo "Recent log entries:"
            tail -5 "$LOG_FILE"
        fi
        return 0
    else
        rm -f "$PID_FILE"
        echo "Daemon not running (stale PID file removed)"
        return 1
    fi
}

case "${1:-}" in
    start)
        start_daemon
        ;;
    stop)
        stop_daemon
        ;;
    status)
        daemon_status
        ;;
    restart)
        stop_daemon 2>/dev/null
        start_daemon
        ;;
    run)
        # Internal: called when daemon starts itself in background
        run_daemon
        ;;
    *)
        echo "Usage: $0 {start|stop|status|restart}"
        exit 1
        ;;
esac
