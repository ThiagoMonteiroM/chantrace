package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/khzaw/chantrace"
)

const maxBufferedEvents = 2048

type backend struct {
	mu     sync.Mutex
	events [maxBufferedEvents]chantrace.Event
	head   int
	count  int
	srv    *http.Server
}

func newBackend(addr string) chantrace.Backend {
	if addr == "" {
		addr = ":4884"
	}

	b := &backend{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", b.handleIndex)
	mux.HandleFunc("/events", b.handleEvents)

	b.srv = &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	go func() {
		err := b.srv.ListenAndServe()
		if err == nil || err == http.ErrServerClosed {
			return
		}
	}()
	return b
}

func (b *backend) HandleEvent(e chantrace.Event) {
	b.mu.Lock()
	idx := (b.head + b.count) % maxBufferedEvents
	b.events[idx] = e
	if b.count < maxBufferedEvents {
		b.count++
	} else {
		b.head = (b.head + 1) % maxBufferedEvents
	}
	b.mu.Unlock()
}

func (b *backend) Close() error {
	if b.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return b.srv.Shutdown(ctx)
}

const timelineDashboardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>chantrace timeline</title>
<style>
:root {
	--bg: #0a0a0a;
	--panel: #111;
	--line: #333;
	--text: #ededed;
	--muted: #666;
	--divider: #555;
	--hover: #1a1a1a;
	--amber: #f59e0b;
	--green: #22c55e;
	--blue: #3b82f6;
	--red: #ef4444;
	--purple: #a855f7;
	--cyan: #06b6d4;
}
* {
	box-sizing: border-box;
}
html,
body {
	margin: 0;
	height: 100%;
	background: var(--bg);
	color: var(--text);
	font-family: "SF Mono", Menlo, Consolas, monospace;
}
.app {
	height: 100%;
	display: flex;
	flex-direction: column;
}
.topbar {
	display: flex;
	align-items: center;
	gap: 12px;
	padding: 10px 14px;
	border-bottom: 1px solid #1b1b1b;
	background: #0d0d0d;
}
.brand {
	font-size: 14px;
	font-weight: 700;
	letter-spacing: 0.02em;
}
.spacer {
	flex: 1 1 auto;
}
.count {
	font-size: 12px;
	color: var(--muted);
	min-width: 96px;
	text-align: right;
}
.filter {
	width: min(42vw, 280px);
	padding: 6px 8px;
	border: 1px solid #2a2a2a;
	background: #121212;
	color: var(--text);
	border-radius: 4px;
	font: inherit;
	font-size: 12px;
}
.toggle {
	padding: 6px 10px;
	border: 1px solid #2a2a2a;
	background: #161616;
	color: var(--text);
	border-radius: 4px;
	cursor: pointer;
	font: inherit;
	font-size: 12px;
}
.toggle:hover {
	background: #1d1d1d;
}
.timeline {
	position: relative;
	flex: 1 1 auto;
	overflow-y: auto;
	padding: 8px 12px 10px 52px;
}
.empty {
	margin-top: 14px;
	color: var(--muted);
	font-size: 12px;
}
.sec {
	position: relative;
	margin: 6px 0 2px;
	padding-top: 5px;
	border-top: 1px solid #1b1b1b;
	color: var(--divider);
	font-size: 11px;
}
.event {
	position: relative;
	margin: 0;
	padding: 1px 8px;
	min-height: 16px;
	background: transparent;
	border-left: none;
	border-radius: 2px;
}
.event:hover {
	background: #141414;
}
.event::before {
	content: "";
	position: absolute;
	left: -30px;
	top: 7px;
	width: 6px;
	height: 6px;
	border-radius: 50%;
	background: var(--kind-color, #888);
	border: 1px solid var(--bg);
}
.event-line {
	display: block;
	font-size: 11px;
	line-height: 1.35;
	white-space: nowrap;
	overflow: hidden;
	text-overflow: ellipsis;
}
.ts {
	display: inline-block;
	width: 92px;
	color: var(--text);
}
.kind {
	color: var(--kind-color, #999);
	font-size: 11px;
	white-space: nowrap;
}
.name {
	font-size: 11px;
	color: var(--text);
	white-space: nowrap;
}
.meta {
	color: var(--muted);
	font-size: 11px;
	white-space: nowrap;
}
.new {
	animation: fade-in 150ms ease-out;
}
.k-make {
	--kind-color: var(--amber);
}
.k-send {
	--kind-color: var(--green);
}
.k-recv {
	--kind-color: var(--blue);
}
.k-close {
	--kind-color: var(--red);
}
.k-select {
	--kind-color: var(--purple);
}
.k-go {
	--kind-color: var(--cyan);
}
.k-unknown {
	--kind-color: #888;
}
@keyframes fade-in {
	from {
		opacity: 0;
	}
	to {
		opacity: 1;
	}
}
@media (max-width: 700px) {
	.topbar {
		flex-wrap: wrap;
		row-gap: 8px;
	}
	.spacer {
		display: none;
	}
	.count {
		order: 4;
		width: 100%;
		text-align: left;
	}
	.filter {
		flex: 1 1 220px;
		width: auto;
	}
}
</style>
</head>
<body>
<div class="app">
	<header class="topbar">
		<div class="brand">chantrace</div>
		<div class="spacer"></div>
		<input id="filter" class="filter" type="text" placeholder="Filter by kind or channel">
		<div id="count" class="count">0 events</div>
		<button id="toggle" class="toggle" type="button">Pause</button>
	</header>
	<main id="timeline" class="timeline">
		<div class="empty">Waiting for events...</div>
	</main>
</div>
<script>
(function () {
	"use strict";

	var KIND_NAMES = [
		"make",
		"register",
		"send→",
		"send✓",
		"recv→",
		"recv✓",
		"close",
		"select→",
		"select✓",
		"range→",
		"range",
		"range-done",
		"go-spawn",
		"go-exit",
		"trace-lost"
	];

	var timelineEl = document.getElementById("timeline");
	var filterEl = document.getElementById("filter");
	var countEl = document.getElementById("count");
	var toggleEl = document.getElementById("toggle");

	var paused = false;
	var followTail = true;
	var lastToken = "";
	var seenTotal = 0;
	var timer = 0;

	function esc(value) {
		return String(value)
			.replace(/&/g, "&amp;")
			.replace(/</g, "&lt;")
			.replace(/>/g, "&gt;");
	}

	function two(n) {
		return n < 10 ? "0" + n : String(n);
	}

	function three(n) {
		if (n < 10) {
			return "00" + n;
		}
		if (n < 100) {
			return "0" + n;
		}
		return String(n);
	}

	function formatSecond(ns) {
		var d = new Date(Math.floor(ns / 1000000));
		return two(d.getHours()) + ":" + two(d.getMinutes()) + ":" + two(d.getSeconds());
	}

	function formatSubsecond(ns) {
		var d = new Date(Math.floor(ns / 1000000));
		return formatSecond(ns) + "." + three(d.getMilliseconds());
	}

	function secondKey(ns) {
		return String(Math.floor(ns / 1000000000));
	}

	function kindName(kind) {
		return KIND_NAMES[kind] || "unknown";
	}

	function kindClass(name) {
		if (name === "make" || name === "register") {
			return "k-make";
		}
		if (name.indexOf("send") === 0) {
			return "k-send";
		}
		if (name.indexOf("recv") === 0 || name.indexOf("range") === 0) {
			return "k-recv";
		}
		if (name === "close" || name === "trace-lost") {
			return "k-close";
		}
		if (name.indexOf("select") === 0) {
			return "k-select";
		}
		if (name.indexOf("go-") === 0) {
			return "k-go";
		}
		return "k-unknown";
	}

	function channelName(e) {
		if (e.ChannelName) {
			return e.ChannelName;
		}
		if (e.GoLabel) {
			return e.GoLabel;
		}
		return "(unnamed)";
	}

	function fileLine(e) {
		if (!e.File) {
			return "";
		}
		var file = String(e.File).split("/").pop();
		if (e.Line > 0) {
			return file + ":" + e.Line;
		}
		return file;
	}

	function token(e) {
		return [
			e.Timestamp,
			e.Kind,
			e.GoroutineID,
			e.OpID,
			e.ChannelID,
			e.ChannelName,
			e.GoLabel,
			e.ValueStr,
			e.Line,
			e.Dropped,
			e.SelectIndex
		].join("|");
	}

	function findNewTokens(events) {
		var fresh = new Set();
		if (!events.length) {
			lastToken = "";
			seenTotal = 0;
			return fresh;
		}
		if (!lastToken) {
			lastToken = token(events[events.length - 1]);
			seenTotal = events.length;
			return fresh;
		}
		var match = -1;
		for (var i = events.length - 1; i >= 0; i--) {
			if (token(events[i]) === lastToken) {
				match = i;
				break;
			}
		}
		var start = match >= 0 ? match + 1 : 0;
		for (var j = start; j < events.length; j++) {
			fresh.add(token(events[j]));
		}
		seenTotal += fresh.size;
		lastToken = token(events[events.length - 1]);
		return fresh;
	}

	function details(e, name) {
		var parts = [];
		if (e.GoroutineID) {
			parts.push("g" + e.GoroutineID);
		}
		if (name === "go-spawn" && e.ParentGID) {
			parts.push("← g" + e.ParentGID);
		}
		if (name === "trace-lost" && e.Dropped > 0) {
			parts.push("dropped " + e.Dropped + " events");
		}
		if (e.ValueStr) {
			parts.push(e.ValueStr);
		}
		if (name === "recv✓" && e.RecvOK === false) {
			parts.push("closed");
		}
		if (name === "select✓" && e.SelectIndex >= 0) {
			parts.push("case " + e.SelectIndex);
		}
		var loc = fileLine(e);
		if (loc) {
			parts.push(loc);
		}
		if (e.BufCap > 0 || e.BufLen > 0) {
			parts.push("buf " + e.BufLen + "/" + e.BufCap);
		}
		return parts.join("  ");
	}

	function updateCount(shown, total) {
		if (total === 0) {
			countEl.textContent = "0 events";
			return;
		}
		if (shown === total) {
			countEl.textContent = seenTotal + " seen (" + total + " buffered)";
			return;
		}
		countEl.textContent = shown + "/" + total + " buffered • " + seenTotal + " seen";
	}

	function render(events) {
		var query = filterEl.value.trim().toLowerCase();
		var filtered = events.filter(function (e) {
			if (!query) {
				return true;
			}
			var kind = kindName(e.Kind).toLowerCase();
			var ch = (e.ChannelName || "").toLowerCase();
			return kind.indexOf(query) >= 0 || ch.indexOf(query) >= 0;
		});

		updateCount(filtered.length, events.length);
		var newTokens = findNewTokens(events);
		var html = [];

		if (!filtered.length) {
			html.push("<div class='empty'>" + (events.length ? "No matching events." : "Waiting for events...") + "</div>");
			timelineEl.innerHTML = html.join("");
			return;
		}

		var lastSec = "";
		for (var i = 0; i < filtered.length; i++) {
			var e = filtered[i];
			var kname = kindName(e.Kind);
			var skey = secondKey(e.Timestamp);
			if (skey !== lastSec) {
				lastSec = skey;
				html.push("<div class='sec'>" + esc(formatSecond(e.Timestamp)) + "</div>");
			}

			var klass = "event " + kindClass(kname);
			if (newTokens.has(token(e))) {
				klass += " new";
			}
			html.push("<article class='" + klass + "'>");
			html.push(
				"<span class='event-line'><span class='ts'>" +
					esc(formatSubsecond(e.Timestamp)) +
					"</span> <span class='kind'>" +
					esc(kname) +
					"</span> <span class='name'>" +
					esc(channelName(e)) +
					"</span> <span class='meta'>" +
					esc(details(e, kname)) +
					"</span></span>",
			);
			html.push("</article>");
		}

		timelineEl.innerHTML = html.join("");
		if (followTail) {
			timelineEl.scrollTop = timelineEl.scrollHeight;
		}
	}

	function schedule() {
		window.clearTimeout(timer);
		if (!paused) {
			timer = window.setTimeout(poll, 500);
		}
	}

	function poll() {
		if (paused) {
			return;
		}
		fetch("/events", { cache: "no-store" })
			.then(function (resp) {
				if (!resp.ok) {
					throw new Error("http " + resp.status);
				}
				return resp.json();
			})
			.then(function (events) {
				if (!Array.isArray(events)) {
					events = [];
				}
				render(events);
			})
			.catch(function () {
				timelineEl.innerHTML = "<div class='empty'>Unable to load /events.</div>";
			})
			.finally(schedule);
	}

	filterEl.addEventListener("input", function () {
		poll();
	});

	toggleEl.addEventListener("click", function () {
		paused = !paused;
		toggleEl.textContent = paused ? "Resume" : "Pause";
		if (!paused) {
			poll();
		} else {
			window.clearTimeout(timer);
		}
	});

	timelineEl.addEventListener("scroll", function () {
		var threshold = 24;
		var distance = timelineEl.scrollHeight - timelineEl.scrollTop - timelineEl.clientHeight;
		followTail = distance <= threshold;
	});

	poll();
})();
</script>
</body>
</html>
`

func (b *backend) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(timelineDashboardHTML))
}

func (b *backend) handleEvents(w http.ResponseWriter, _ *http.Request) {
	b.mu.Lock()
	events := make([]chantrace.Event, b.count)
	for i := range b.count {
		events[i] = b.events[(b.head+i)%maxBufferedEvents]
	}
	b.mu.Unlock()

	data, err := json.Marshal(events)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func init() {
	chantrace.RegisterBackendFactory("web", func(addr string) chantrace.Backend {
		return newBackend(addr)
	})
}
