# Render a terminal session as a PNG for the docs (docs/assets/terminal/).
#
#   python3 docs/assets/termshot.py OUT.png COLS ROWS 'command' [keys...]
#
# Runs the command in a COLS x ROWS pty, types each key after a short pause
# ("@0.5" waits half a second, "!" snapshots the screen without waiting for
# exit, for full-screen programs), emulates the screen with pyte, and
# screenshots it in a terminal window drawn in the brand colors.
# Needs: pip install pyte playwright (and a Chromium; CHROME=/path overrides).
import sys, os, pty, time, select, fcntl, termios, struct, html, re
import pyte
from playwright.sync_api import sync_playwright

out, cols, rows, cmd, keys = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4], sys.argv[5:]
class Screen(pyte.HistoryScreen):
    # pyte lacks REP (CSI n b, repeat the last character), which Bubble Tea's
    # renderer uses for runs of the same character, such as borders.
    last = " "
    def draw(self, data):
        if data:
            self.last = data[-1]
        super().draw(data)
    def rep(self, n=1, *args, **kw):
        self.draw(self.last * max(1, n))

pyte.Stream.csi["b"] = "rep"
screen = Screen(cols, rows, history=2000)
stream = pyte.ByteStream(screen)
screen.set_mode(pyte.modes.LNM)

if os.environ.get("TERMSHOT_TMUX"):
    # tmux is a real terminal: let it run the program, then draw its screen.
    import subprocess, shlex
    sock = "termshot%d" % os.getpid()
    tm = lambda *a: subprocess.run(["tmux", "-L", sock] + list(a), capture_output=True, text=True).stdout
    tm("new-session", "-d", "-x", str(cols), "-y", str(rows), "bash", "-c", cmd + "; sleep 600")
    time.sleep(1.5)
    for k in keys:
        if k == "!":
            break
        if k.startswith("@"):
            time.sleep(float(k[1:])); continue
        tm("send-keys", "-l", k.encode().decode("unicode_escape")); time.sleep(0.6)
    time.sleep(0.8)
    shot = tm("capture-pane", "-p", "-e", "-S", "-2000")
    tm("kill-server")
    screen = Screen(cols, max(rows, shot.count("\n") + 1), history=0)
    stream = pyte.ByteStream(screen)
    screen.set_mode(pyte.modes.LNM)
    stream.feed(shot.encode())
    rows = screen.lines
    snap = False
else:
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["COLUMNS"], os.environ["LINES"] = str(cols), str(rows)
        os.execvp("bash", ["bash", "-c", cmd])
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    raw = bytearray()
    def pump(t):
        end = time.time() + t
        while time.time() < end:
            r, _, _ = select.select([fd], [], [], 0.05)
            if r:
                try:
                    b = os.read(fd, 65536)
                except OSError:
                    return False
                if not b:
                    return False
                raw.extend(b); stream.feed(re.sub(rb'\x1b\[[<>=?][0-9;$]*[a-zA-Z]', b'', b))
        return True
    pump(1.2)
    snap = False
    for k in keys:
        if k == "!":
            snap = True; break
        if k.startswith("@"):
            pump(float(k[1:])); continue
        os.write(fd, k.encode().decode("unicode_escape").encode()); pump(0.6)
    while not snap and pump(0.3):
        try:
            if os.waitpid(pid, os.WNOHANG)[0]:
                pump(0.2); break
        except ChildProcessError:
            break

# Gather lines: scrolled-off history then the screen; trim trailing blanks.
def line_cells(line):
    return [line[x] for x in range(cols)]
lines = [] if snap else [list(l.values()) if isinstance(l, dict) else [l[x] for x in range(cols)] for l in screen.history.top]
lines += [line_cells(screen.buffer[y]) for y in range(rows)]
def blank(l): return all(c.data == " " and c.bg == "default" for c in l)
while lines and blank(lines[-1]): lines.pop()
while lines and blank(lines[0]): lines.pop(0)

NAMED = {"black": "#0e1020", "red": "#f2836f", "green": "#6fdccb", "brown": "#f4b443", "yellow": "#f4b443",
         "blue": "#a99cf5", "magenta": "#a99cf5", "cyan": "#6fdccb", "white": "#f6f2e9"}
def col(c, default):
    if c == "default": return default
    if c in NAMED: return NAMED[c]
    if re.fullmatch(r"[0-9a-fA-F]{6}", c): return "#" + c
    return default
rows_html = []
for l in lines:
    parts, prev, buf = [], None, ""
    for c in l:
        key = (c.fg, c.bg, c.bold, c.italics, c.underscore, c.reverse)
        if key != prev and buf:
            parts.append((prev, buf)); buf = ""
        prev = key; buf += c.data
    if buf: parts.append((prev, buf))
    h = ""
    for (fg, bg, b, it, u, rev), text in parts:
        f, g = col(fg, "#d9dbe6"), col(bg, "transparent")
        if rev: f, g = (g if g != "transparent" else "#0e1020"), f
        st = f"color:{f};background:{g};" + ("font-weight:700;" if b else "") + ("font-style:italic;" if it else "") + ("text-decoration:underline;" if u else "")
        h += f'<span style="{st}">{html.escape(text)}</span>'
    rows_html.append(h.rstrip() or " ")
fonts = os.path.join(os.path.dirname(os.path.abspath(__file__)), "src", "fonts")
page = f"""<html><head><style>
@font-face {{ font-family: JB; src: url(file://{fonts}/JetBrainsMono-Variable.woff2); font-weight: 100 800; }}
body {{ margin:0; background: transparent; padding: 28px; display:inline-block; }}
.win {{ background:#0e1020; border:1px solid #252a4a; border-radius:12px; box-shadow:0 18px 50px rgba(0,0,0,.45); overflow:hidden; display:inline-block; }}
.bar {{ height:34px; background:#151830; display:flex; align-items:center; gap:8px; padding:0 14px; border-bottom:1px solid #252a4a; }}
.dot {{ width:12px; height:12px; border-radius:50%; }}
.title {{ color:#767c99; font: 500 12px JB; margin-left: 10px; }}
pre {{ margin:0; padding:18px 22px 22px; font: 14px/1.45 JB, "DejaVu Sans Mono", monospace; font-variant-ligatures: none; color:#d9dbe6; white-space:pre; }}
</style></head><body><div class="win"><div class="bar"><span class="dot" style="background:#f2836f"></span><span class="dot" style="background:#f4b443"></span><span class="dot" style="background:#6fdccb"></span><span class="title">{html.escape(os.environ.get('SHOT_TITLE', cmd))}</span></div><pre>{chr(10).join(rows_html)}</pre></div></body></html>"""
with sync_playwright() as p:
    b = p.chromium.launch(executable_path=os.environ.get("CHROME") or None, args=["--no-sandbox", "--allow-file-access-from-files"])
    pg = b.new_page(viewport={"width": 1600, "height": 800}, device_scale_factor=2)
    import tempfile
    with tempfile.NamedTemporaryFile("w", suffix=".html", delete=False) as f:
        f.write(page)
    pg.goto("file://" + f.name); pg.wait_for_timeout(300)
    pg.locator("body").screenshot(path=out, omit_background=True)
    os.unlink(f.name)
    b.close()
print("wrote", out, len(lines), "lines")
