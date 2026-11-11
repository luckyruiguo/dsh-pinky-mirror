#!/usr/bin/env python3
"""Screen-on check of Pinky's window: the New agent card, an approval in the
main window and in the floating panel, taking over from the mirror, handing
back, and Stop all. It drives build/pinky-e2e through MyGo's automation
socket and acts as the agent over MCP, on an emulator only.

Run it with the screen on and unlocked (a locked Mac stops drawing windows),
after `make e2e`, holding the emulator:

    scripts/devlock.sh emulator-5560 scripts/screencheck.py . /tmp/pinky-check

It turns the emulator's Airplane mode on and back off. Captures and a log go
to OUTDIR.
"""
import base64
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import threading
import time

REPO, OUT = sys.argv[1], sys.argv[2]
SERIAL = sys.argv[3] if len(sys.argv) > 3 else "emulator-5560"
HOME = "/tmp/pk-check"
SOCK = HOME + "/auto.sock"
BIN = os.path.join(REPO, "build/pinky-e2e")
os.makedirs(OUT, exist_ok=True)
results = []


def log(msg):
    line = time.strftime("%H:%M:%S ") + msg
    print(line, flush=True)
    with open(os.path.join(OUT, "log.txt"), "a") as f:
        f.write(line + "\n")


def result(name, ok, detail=""):
    results.append((name, ok, detail))
    log(("PASS " if ok else "FAIL ") + name + (" · " + detail if detail else ""))


# --- automation socket -------------------------------------------------------

_aid = 0


def auto(method, params=None, timeout=30):
    global _aid
    _aid += 1
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(timeout)
    s.connect(SOCK)
    req = {"id": _aid, "method": method}
    if params is not None:
        req["params"] = params
    s.sendall((json.dumps(req) + "\n").encode())
    buf = b""
    while not buf.endswith(b"\n"):
        chunk = s.recv(1 << 20)
        if not chunk:
            break
        buf += chunk
    s.close()
    resp = json.loads(buf)
    if "error" in resp:
        raise RuntimeError("%s: %s" % (method, resp["error"]))
    return resp["result"]


def windows():
    return {w["title"]: w for w in auto("windows")["windows"]}


def win_id(title):
    w = windows().get(title)
    return w["id"] if w else None


def nodes(win):
    try:
        return auto("tree", {"window": win})["nodes"]
    except RuntimeError:
        return []


def find(win, text, role=None):
    out = []
    for n in nodes(win):
        lab = n.get("label", "")
        if text.lower() in lab.lower() and (role is None or n.get("role") == role) and n.get("bounds"):
            out.append(n)
    return out


def center(n):
    b = n["bounds"]
    return b["x"] + b["width"] / 2, b["y"] + b["height"] / 2


def click(win, n):
    x, y = center(n)
    auto("input", {"window": win, "events": [{"type": "click", "x": x, "y": y}]})


def capture(win, name):
    try:
        r = auto("capture", {"window": win})
        path = os.path.join(OUT, name + ".png")
        with open(path, "wb") as f:
            f.write(base64.b64decode(r["png"]))
        return path
    except RuntimeError as e:
        log("capture %s failed: %s" % (name, e))


def wait_for(fn, what, timeout=20):
    end = time.time() + timeout
    while time.time() < end:
        v = fn()
        if v:
            return v
        time.sleep(0.25)
    log("timed out waiting for " + what)
    return None


def menu(path):
    auto("menu", {"window": win_id("Pinky"), "path": path})


# --- MCP agent ---------------------------------------------------------------

class Agent:
    def __init__(self, name):
        env = dict(os.environ, PINKY_HOME=HOME)
        self.p = subprocess.Popen([BIN, "mcp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=subprocess.DEVNULL, env=env, text=True, bufsize=1)
        self.id = 0
        self.waiting = {}
        self.lock = threading.Lock()
        threading.Thread(target=self._read, daemon=True).start()
        self.request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                    "clientInfo": {"name": name, "version": "check"}})
        self.notify("notifications/initialized")
        self.refs = {}

    def _read(self):
        for line in self.p.stdout:
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if "id" in msg and msg["id"] in self.waiting:
                ev, box = self.waiting.pop(msg["id"])
                box.append(msg)
                ev.set()

    def notify(self, method, params=None):
        m = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            m["params"] = params
        self.p.stdin.write(json.dumps(m) + "\n")

    def request(self, method, params, timeout=120):
        with self.lock:
            self.id += 1
            i = self.id
            ev, box = threading.Event(), []
            self.waiting[i] = (ev, box)
            self.p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": i, "method": method, "params": params}) + "\n")
        if not ev.wait(timeout):
            raise RuntimeError("no answer to " + method)
        msg = box[0]
        if "error" in msg:
            raise RuntimeError(json.dumps(msg["error"]))
        return msg["result"]

    def tool(self, name, args):
        r = self.request("tools/call", {"name": name, "arguments": args})
        text = "\n".join(c.get("text", "") for c in r.get("content", []))
        for m in re.finditer(r'\[(\d+)\] \S+ "([^"]*)"', text):
            self.refs[m.group(2)] = int(m.group(1))
        return text

    def ref(self, label):
        if label in self.refs:
            return self.refs[label]
        for k, v in self.refs.items():
            if label.lower() in k.lower():
                return v
        return None

    def background(self, name, args):
        box = {}

        def run():
            try:
                box["text"] = self.tool(name, args)
            except Exception as e:  # noqa: BLE001
                box["text"] = "ERROR " + str(e)
        t = threading.Thread(target=run, daemon=True)
        t.start()
        box["thread"] = t
        return box

    def close(self):
        try:
            self.p.stdin.close()
            self.p.wait(5)
        except Exception:  # noqa: BLE001
            self.p.kill()


def first_line(t):
    return (t or "").strip().splitlines()[0][:160] if t else ""


def main():
    shutil.rmtree(HOME, ignore_errors=True)
    os.makedirs(HOME)
    # The check finds controls by their English labels, whatever the Mac's
    # language is.
    env = dict(os.environ, PINKY_HOME=HOME, PINKY_DEVICES=SERIAL, MYGO_AUTOMATION=SOCK, MYGO_LOCALE="en")
    app = subprocess.Popen([BIN, "--approval-timeout", "120s"], env=env,
                           stdout=open(os.path.join(OUT, "app.log"), "w"), stderr=subprocess.STDOUT)
    try:
        if not wait_for(lambda: os.path.exists(SOCK) and win_id("Pinky"), "the window", 30):
            result("app starts", False)
            return
        main_w = win_id("Pinky")
        menu(["Debug", "Show Main Window"])
        dev = wait_for(lambda: find(main_w, "screen, live"), "the phone's mirror", 40)
        result("phone and mirror shown", bool(dev))
        time.sleep(3)
        capture(main_w, "01-phone")

        # 1. New agent card in the main window.
        ag = Agent("claude-code")
        call = ag.background("phone_connect", {"task": "Screen-on check", "device": SERIAL})
        btn = wait_for(lambda: find(main_w, "Approve", "button"), "the New agent card", 20)
        capture(main_w, "02-new-agent")
        if btn:
            click(main_w, btn[0])
        call["thread"].join(60)
        res = call.get("text", "")
        result("New agent card: Approve connects the agent", "Connected to" in res, first_line(res))

        # 2. A Settings change asks; Deny in the main window.
        ag.tool("phone_launch", {"app": "Settings"})
        time.sleep(1)
        ag.tool("phone_observe", {})
        net = ag.ref("Network & internet") or ag.ref("Network")
        if net:
            ag.tool("phone_tap", {"ref": net})
            time.sleep(1)
            ag.tool("phone_observe", {})
        air = ag.ref("Airplane mode") or ag.ref("Airplane")
        result("found Airplane mode on the emulator", bool(air))
        if air:
            call = ag.background("phone_tap", {"ref": air})
            deny = wait_for(lambda: find(main_w, "Deny", "button"), "the approval card", 20)
            capture(main_w, "03-approval-main")
            if deny:
                click(main_w, deny[0])
            call["thread"].join(60)
            res = call.get("text", "")
            result("approval in the main window: Deny", "denied" in res.lower(), first_line(res))

            # 3. The same in the floating panel, with the main window hidden.
            menu(["Debug", "Hide Main Window"])
            time.sleep(1)
            call = ag.background("phone_tap", {"ref": air})
            panel = wait_for(lambda: windows().get("Pinky approval", {}).get("visible") and win_id("Pinky approval"), "the panel", 20)
            allow = wait_for(lambda: find(panel, "Allow", "button"), "Allow in the panel", 20) if panel else None
            if panel:
                capture(panel, "04-panel")
            if allow:
                click(panel, allow[0])
            call["thread"].join(60)
            res = call.get("text", "")
            result("floating panel shows the approval", bool(allow))
            result("floating panel: Allow runs the step", "settled" in res.lower() or "uncertain" in res.lower(), first_line(res))
            # Turn Airplane mode back off, through the panel again.
            call = ag.background("phone_tap", {"ref": air})
            allow = wait_for(lambda: find(panel, "Allow", "button"), "Allow in the panel", 20) if panel else None
            if allow:
                click(panel, allow[0])
            call["thread"].join(60)
            menu(["Debug", "Show Main Window"])
            time.sleep(1)

        # 4. Take over from the mirror, then hand back.
        scr = find(main_w, "screen, live")
        if scr:
            click(main_w, scr[0])
            banner = wait_for(lambda: find(main_w, "You're in control"), "the control banner", 10)
            capture(main_w, "05-in-control")
            result("clicking the mirror takes over", bool(banner))
            res = ag.tool("phone_observe", {})
            res2 = ag.tool("phone_key", {"key": "back"})
            result("the agent is paused", "paused" in (res + res2).lower() or "took_over" in (res + res2).lower(),
                   first_line(res2))
            hb = find(main_w, "Hand back", "button")
            if hb:
                click(main_w, hb[0])
            time.sleep(1)
            res = ag.tool("phone_key", {"key": "back"})
            result("Hand back lets the agent go on", "settled" in res.lower() or "uncertain" in res.lower(), first_line(res))

        # 5. Stop all.
        stop = find(main_w, "Stop all", "button")
        if stop:
            click(main_w, stop[0])
        time.sleep(1)
        res = ag.tool("phone_key", {"key": "home"})
        result("Stop all ends the agent's task", "stopped" in res.lower(), first_line(res))
        capture(main_w, "06-stopped")
        ag.close()
    finally:
        app.terminate()
        try:
            app.wait(10)
        except Exception:  # noqa: BLE001
            app.kill()
        with open(os.path.join(OUT, "results.json"), "w") as f:
            json.dump(results, f, indent=1)
        log("done: %d passed, %d failed" % (sum(1 for r in results if r[1]), sum(1 for r in results if not r[1])))


if __name__ == "__main__":
    main()
