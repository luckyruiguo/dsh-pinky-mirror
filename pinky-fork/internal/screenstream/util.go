package screenstream

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

func writeJSON(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }

func itoa(n int) string { return strconv.Itoa(n) }

// ServeHTTP 路由 /screen/*。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Cache-Control", "no-store")

	switch r.URL.Path {
	case "/screen/stream", "/screen/stream/":
		s.serveMJPEG(w, r)
	case "/screen/view", "/screen/view/":
		// 独立查看页：给 iframe 直接嵌入，不依赖宿主前端框架
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(viewPage))
	case "/screen/frame.jpg":
		f, _ := s.Snapshot()
		if f == nil {
			http.Error(w, "no frame", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(f)
	case "/screen/health":
		f, at := s.Snapshot()
		age := time.Since(at)
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]any{
			// connected 表示链路正常（有画面可达）；静态画面时
			// 手机本来就不发新帧，age 变大不代表断流。
			"connected": f != nil,
			"live":      f != nil && age < s.opt.MaxAge,
			"seq":      s.Seq(),
			"frameAge": age.Milliseconds(),
			"takeover": s.takeover.Load(),
			"fps":      s.opt.FPS,
			"encoder":  s.ffmpeg != "",
		})
	case "/screen/key":
		// 导航键：POST /screen/key?k=back|home|recents
		// 走 Gateway.Key，与 Pinky 客户端点按键是同一条路径
		k := r.URL.Query().Get("k")
		var key driver.Key
		switch k {
		case "back":
			key = driver.KeyBack
		case "home":
			key = driver.KeyHome
		case "recents":
			key = driver.KeyRecents
		default:
			http.Error(w, "unknown key: "+k, http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := s.pressKey(key); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "key": k})
	case "/screen/takeover":
		if r.Method == http.MethodPost {
			s.Takeover(r.URL.Query().Get("on") == "1")
		}
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]any{"takeover": s.takeover.Load()})
	default:
		http.NotFound(w, r)
	}
}

// Seq 返回已发出的帧数。
func (s *Server) Seq() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seq
}

func (s *Server) serveMJPEG(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=pinkyframe")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch := make(chan []byte, 3)
	s.vmu.Lock()
	s.viewers[ch] = struct{}{}
	s.vmu.Unlock()
	defer func() {
		s.vmu.Lock()
		delete(s.viewers, ch)
		s.vmu.Unlock()
	}()

	if f, _ := s.Snapshot(); f != nil {
		writePart(w, f)
		fl.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case f := <-ch:
			writePart(w, f)
			fl.Flush()
		}
	}
}

func writePart(w http.ResponseWriter, f []byte) {
	w.Write([]byte("\r\n--pinkyframe\r\nContent-Type: image/jpeg\r\nContent-Length: "))
	w.Write([]byte(itoa(len(f))))
	w.Write([]byte("\r\n\r\n"))
	w.Write(f)
}

// viewPage 是手机画面查看页（自包含，可单独打开或嵌入 iframe）。
// 外框参数完全照搬 Pinky 桌面客户端（ui/bezel.go、ui/phone.go）：
//   边框厚度 9px、内圆角 = 短边×0.137（Android）、外圆角 = 内圆角+9-1
//   近阴影 (0,2,6)、远阴影 (0,18,44)、金属边缘 1px
//   Pinky 不画刘海 —— 画面本身已含手机顶部
const viewPage = `<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Pinky 手机画面</title>
<style>
  :root{
    --stage:#f5f5f7;
    --phone:#111114;      /* 机身材质 */
    --rim:rgba(255,255,255,.14);   /* 金属边缘 */
    --shadow-lo:rgba(16,16,24,.10);
    --shadow-hi:rgba(16,16,24,.13);
  }
  @media (prefers-color-scheme: dark){
    :root{ --stage:#17171b; --phone:#0a0a0c; --rim:rgba(255,255,255,.10); }
  }
  html,body{margin:0;padding:0;height:100%;overflow:hidden;
    background:var(--stage);font-family:system-ui,-apple-system,sans-serif}

  /* 舞台：留白居中，画面不铺满 */
  #stage{display:flex;align-items:center;justify-content:center;
    height:100%;width:100%;padding:16px;box-sizing:border-box}
  /* 缩放：占舞台的比例（可通过 --scale 调整） */
  :root{ --scale: 0.77; --bezel: 6.3px; }
  .device-wrap{display:flex;align-items:center;justify-content:center;
    width:100%;height:100%}

  /* 机身：Pinky 的 9px 边框 + 双阴影 + 金属边 */
  .device{
    position:relative;display:inline-flex;   /* 尺寸随内容 */
    background:var(--phone);
    border-radius:var(--outer-r,40px);
    padding:var(--bezel,6.3px);    /* bezel：Pinky 原生 9px 的 70% */
    box-sizing:border-box;
    box-shadow:
      0 0 0 1px var(--rim),        /* Rim(0,0,0,1) 金属边缘 */
      0 2px 6px var(--shadow-lo),  /* Shadow(0,2,6) 近 */
      0 18px 44px var(--shadow-hi);/* Shadow(0,18,44) 远 */
  }

  /* 屏幕：内圆角 = 短边×0.137 */
  .screen{
    position:relative;
    border-radius:var(--inner-r,26px);
    overflow:hidden;background:#000;
    line-height:0;                 /* 去掉 img 底部空隙 */
  }
  /* 画面按真实像素比例显示，尺寸由 JS 按舞台计算，边框恰好 9px */
  .screen img{display:block;max-width:none;max-height:none}

  /* 状态浮层 */
  #bar{
    position:absolute;left:50%;bottom:12px;transform:translateX(-50%);
    display:flex;align-items:center;gap:5px;
    padding:3px 10px;border-radius:999px;z-index:4;
    font-size:11px;color:#fff;
    background:rgba(0,0,0,.55);backdrop-filter:blur(8px);
    pointer-events:none;opacity:0;transition:opacity .25s;
  }
  #bar.show{opacity:1}
  #dot{width:5px;height:5px;border-radius:50%;background:#34c759}
  #dot.off{background:#ff453a}

  /* 导航键栏（参考 Pinky 的手机下方按键） */
  #keys{
    display:flex;align-items:center;justify-content:center;gap:6px;
    padding:8px 0 4px;flex:0 0 auto;
  }
  .key{
    display:inline-flex;align-items:center;justify-content:center;
    width:38px;height:32px;border-radius:9px;
    border:1px solid rgba(0,0,0,.10);
    background:rgba(255,255,255,.72);
    color:#3a3a3e;cursor:pointer;
    transition:background .12s,transform .08s;
    -webkit-tap-highlight-color:transparent;
  }
  @media (prefers-color-scheme: dark){
    .key{background:rgba(255,255,255,.09);border-color:rgba(255,255,255,.09);color:#d8d8dc}
  }
  .key:hover{background:rgba(255,255,255,.95)}
  @media (prefers-color-scheme: dark){ .key:hover{background:rgba(255,255,255,.16)} }
  .key:active{transform:scale(.94)}
  .key svg{width:17px;height:17px;display:block}
  .key.busy{opacity:.45;pointer-events:none}

  /* 未连接提示 */
  #err{display:none;position:absolute;inset:0;align-items:center;justify-content:center;
    flex-direction:column;gap:8px;color:#8a8a8e;font-size:13px;text-align:center;padding:20px}
  #err small{font-size:11px;opacity:.65;line-height:1.5}
</style>
</head>
<body>
<div id="stage">
  <div class="device-wrap" style="flex-direction:column">
  <div class="device" id="dev">
    <div class="screen" id="scr">
      <img id="v" alt="">
      <div id="bar"><span id="dot"></span><span id="st">连接中…</span></div>
      <div id="err"><div>画面未连接</div><small>确认手机已亮屏且未被休眠</small></div>
    </div>
  </div>
  <div id="keys">
    <button class="key" data-k="back" title="返回" aria-label="返回">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M15 5 8 12l7 7"/></svg>
    </button>
    <button class="key" data-k="home" title="主页" aria-label="主页">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="7.5"/></svg>
    </button>
    <button class="key" data-k="recents" title="最近任务" aria-label="最近任务">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><rect x="6" y="6" width="12" height="12" rx="2.2"/></svg>
    </button>
  </div>
  </div>
</div>
<script>
(function(){
  var img=document.getElementById('v'), st=document.getElementById('st'),
      dot=document.getElementById('dot'), bar=document.getElementById('bar'),
      err=document.getElementById('err'), dev=document.getElementById('dev'),
      n=0, hideTimer=null;

  // 按画面真实宽高比 + 舞台可用空间，算出图片尺寸。
  // 外框 = 图片 + 四周各 9px 边框，所以上下左右的黑边永远一样宽。
  // 边框宽度从 CSS 变量读取，保证 CSS 与 JS 用同一个值
  function bezelPx(){
    var v = parseFloat(getComputedStyle(dev).getPropertyValue('--bezel'));
    return isNaN(v) ? 6.3 : v;
  }
  function layout(){
    var w = img.naturalWidth, h = img.naturalHeight;
    var stage = document.getElementById('stage');
    if(!w || !h || !stage) return;

    var scale = parseFloat(
      getComputedStyle(document.documentElement).getPropertyValue('--scale')
    ) || 0.77;

    // 舞台可用区域（去掉 padding）
    var cs = getComputedStyle(stage);
    var availW = stage.clientWidth  - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    var availH = stage.clientHeight - parseFloat(cs.paddingTop)  - parseFloat(cs.paddingBottom);
    availW *= scale; availH *= scale;

    // 扣掉边框后图片可用空间
    var bezel = bezelPx();
    var maxW = Math.max(1, availW - bezel * 2);
    var maxH = Math.max(1, availH - bezel * 2);

    // 等比缩放到刚好放进
    var ratio = Math.min(maxW / w, maxH / h);
    var imgW = Math.round(w * ratio);
    var imgH = Math.round(h * ratio);

    img.style.width  = imgW + 'px';
    img.style.height = imgH + 'px';

    // 圆角：Pinky 规则 = 短边×0.137，外圆角 = 内+9-1
    var shortSide = Math.min(imgW, imgH);
    var inner = Math.max(4, shortSide * 0.137);
    dev.style.setProperty('--inner-r', inner.toFixed(1) + 'px');
    dev.style.setProperty('--outer-r', (inner + bezel - 1).toFixed(1) + 'px');
  }

  window.addEventListener('resize', layout);

  function status(text, ok){
    st.textContent=text; dot.className = ok ? '' : 'off';
    bar.classList.add('show');
    clearTimeout(hideTimer);
    if(ok) hideTimer=setTimeout(function(){ bar.classList.remove('show'); }, 1600);
  }
  function connect(){
    n++; img.src='/screen/stream?t='+n; status('连接中…', true);
  }
  img.onload=function(){ err.style.display='none'; status('实时', true); layout(); };
  img.onerror=function(){
    status('未连接', false); err.style.display='flex';
    setTimeout(connect, 2000);
  };
  setInterval(function(){
    fetch('/screen/health',{cache:'no-store'})
      .then(function(r){return r.json();})
      .then(function(j){ if(j.connected){ err.style.display='none'; } else { status('未连接', false); } })
      .catch(function(){ status('未连接', false); });
  }, 3000);
  // 导航键：POST /screen/key?k=back|home|recents
  Array.prototype.forEach.call(document.querySelectorAll('.key'), function(btn){
    btn.addEventListener('click', function(){
      var k = btn.getAttribute('data-k');
      if(!k) return;
      btn.classList.add('busy');
      fetch('/screen/key?k=' + encodeURIComponent(k), { method: 'POST' })
        .then(function(r){ return r.json(); })
        .then(function(j){ if(!j.ok) console.warn('key failed:', j.error); })
        .catch(function(e){ console.warn('key error:', e); })
        .then(function(){ setTimeout(function(){ btn.classList.remove('busy'); }, 220); });
    });
  });

  connect();
})();
</script>
</body>
</html>
`
