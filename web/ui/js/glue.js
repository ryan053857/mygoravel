// web/ui 的衔接脚本：把框架的事件交给 shadcn-templ 组件，并按需加载扩展组件。
// 每一项只在页面用到对应部件时生效：不用 Toaster 的页面自己监听 web:flash。
(function () {
  var script = document.currentScript;

  // 提示（web.Flash 和局部请求的错误）：页面中有 Toaster 时交给 toast。
  document.addEventListener("web:flash", function (e) {
    if (!document.querySelector("[data-web-ui-toaster]") || !window.templ || !window.templ.toast) return;
    e.detail.messages.forEach(function (m) {
      window.templ.toast.add(m);
    });
  });

  // 服务端关闭对话框（web.CloseDialog）。
  document.addEventListener("web:dialog-close", function (e) {
    if (window.templ && window.templ.dialog) window.templ.dialog.close(e.detail.id);
  });

  // 暗色模式：点击带 data-web-ui-theme-toggle 的元素切换，选择记在 localStorage 中。
  document.addEventListener("click", function (e) {
    if (!e.target.closest || !e.target.closest("[data-web-ui-theme-toggle]")) return;
    var dark = document.documentElement.classList.toggle("dark");
    try {
      localStorage.setItem("theme", dark ? "dark" : "light");
    } catch (err) {}
  });

  // 扩展组件：页面出现选择器匹配的元素时加载对应脚本，每个只加载一次。
  var pending = JSON.parse((script && script.getAttribute("data-extras")) || "{}");
  function load() {
    Object.keys(pending).forEach(function (selector) {
      if (!document.querySelector(selector)) return;
      var s = document.createElement("script");
      s.src = pending[selector];
      if (script && script.nonce) s.nonce = script.nonce;
      document.head.appendChild(s);
      delete pending[selector];
    });
    if (Object.keys(pending).length === 0 && observer) observer.disconnect();
  }
  var observer = null;
  if (Object.keys(pending).length > 0) {
    observer = new MutationObserver(load);
    observer.observe(document.documentElement, { childList: true, subtree: true });
    load();
  }
})();
