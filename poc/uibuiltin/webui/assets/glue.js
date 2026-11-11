// webui 的衔接脚本：把框架的事件交给 shadcn-templ 组件。只在页面使用了对应部件时生效，
// 区域可以不用 Toaster，自己监听 web:flash。
(function () {
  // 提示（web.Flash）：页面中有 Toaster 时交给 toast。
  document.addEventListener("web:flash", function (e) {
    if (!document.querySelector("[data-webui-toaster]") || !window.templ || !window.templ.toast) return;
    e.detail.messages.forEach(function (m) { window.templ.toast.add(m); });
  });
  // 服务端关闭对话框（CloseDialog）。
  document.addEventListener("web:dialog-close", function (e) {
    if (window.templ && window.templ.dialog) window.templ.dialog.close(e.detail.id);
  });
  // 主题切换：带 data-webui-theme-toggle 的元素切换暗色模式并记住选择。
  document.addEventListener("click", function (e) {
    if (!e.target.closest("[data-webui-theme-toggle]")) return;
    var dark = document.documentElement.classList.toggle("dark");
    try { localStorage.setItem("theme", dark ? "dark" : "light"); } catch (err) {}
  });
})();
