// web/ui 的衔接脚本：把框架的事件交给 shadcn-templ 组件，并按需加载扩展组件。
// 每一项只在页面用到对应部件时生效：不用 Toaster 的页面自己监听 web:flash。
(function () {
  var script = document.currentScript;

  // 提示（局部请求的错误）：页面中有 Toaster 时交给 toast。
  document.addEventListener("web:flash", function (e) {
    if (!document.querySelector("[data-web-ui-toaster]") || !window.templ || !window.templ.toast) return;
    e.detail.messages.forEach(function (m) {
      window.templ.toast.add(m);
    });
  });

  // 暗色模式：点击带 data-web-ui-theme-toggle 的元素切换，选择记在 localStorage 中。
  document.addEventListener("click", function (e) {
    if (!e.target.closest || !e.target.closest("[data-web-ui-theme-toggle]")) return;
    var dark = document.documentElement.classList.toggle("dark");
    try {
      localStorage.setItem("theme", dark ? "dark" : "light");
    } catch (err) {}
  });

  // hx-confirm：页面中有 ui.Confirm 时，用确认对话框代替浏览器的确认框。
  // 点击确认后发出请求；对话框以其他方式关闭（取消、Esc、点击遮罩）时放弃请求。
  var CONFIRM = "web-ui-confirm";
  var confirming = null;
  document.addEventListener("htmx:confirm", function (e) {
    var title = document.querySelector("[data-web-ui-confirm-title]");
    var action = document.querySelector("[data-web-ui-confirm-action]");
    if (!title || !action || !window.templ || !window.templ.dialog) return;
    e.preventDefault();
    if (confirming) confirming.dropRequest();
    var src = e.detail.ctx.sourceElement;
    var description = document.querySelector("[data-web-ui-confirm-description]");
    title.textContent = e.detail.ctx.confirm;
    if (description) {
      description.textContent = src.getAttribute("data-confirm-description") || "";
      description.hidden = !description.textContent;
    }
    action.textContent = src.getAttribute("data-confirm-action") || action.getAttribute("data-web-ui-confirm-action");
    var destructive = src.getAttribute("data-confirm-variant") === "destructive";
    action.classList.toggle("cn-button-variant-destructive", destructive);
    action.classList.toggle("cn-button-variant-default", !destructive);
    confirming = e.detail;
    window.templ.dialog.open(CONFIRM);
  });
  document.addEventListener("click", function (e) {
    if (!confirming || !e.target.closest || !e.target.closest("[data-web-ui-confirm-action]")) return;
    var confirmed = confirming;
    confirming = null;
    window.templ.dialog.close(CONFIRM);
    confirmed.issueRequest();
  });
  document.addEventListener("dialog-close", function () {
    if (!confirming || window.templ.dialog.isOpen(CONFIRM)) return;
    confirming.dropRequest();
    confirming = null;
  });

  // 请求进行中：发起请求的按钮，或提交表单的按钮，带上 aria-busy 并禁用，请求结束后恢复，避免重复提交。
  // 元素自己写了 hx-disable 时由 htmx 处理。
  document.addEventListener(
    "submit",
    function (e) {
      if (e.target && e.target.tagName === "FORM") e.target._webUISubmitter = e.submitter || e.target.querySelector("[type=submit]");
    },
    true,
  );
  document.addEventListener("htmx:before:request", function (e) {
    var ctx = e.detail.ctx;
    var src = ctx.sourceElement;
    if (!src || !src.matches || src.hasAttribute("hx-disable")) return;
    var el = null;
    if (src.matches("button, [data-slot=button]")) el = src;
    else if (src.tagName === "FORM") {
      el = src._webUISubmitter || null; // 只处理由提交触发的请求，输入时自动搜索的表单不受影响
      src._webUISubmitter = null;
    }
    if (!el || el.disabled || el.getAttribute("aria-busy") === "true") return;
    el.setAttribute("aria-busy", "true");
    if (el.tagName === "BUTTON") el.disabled = true;
    ctx.webUIBusy = el;
  });
  document.addEventListener("htmx:finally:request", function (e) {
    var el = e.detail.ctx.webUIBusy;
    if (!el) return;
    el.removeAttribute("aria-busy");
    if (el.tagName === "BUTTON") el.disabled = false;
  });

  // 文档（web/docs）：复制代码块。
  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest("[data-docs-copy]");
    if (!btn || !navigator.clipboard) return;
    var pre = btn.parentElement.querySelector("pre");
    navigator.clipboard.writeText(pre.textContent.replace(/\n$/, "")).then(function () {
      btn.setAttribute("data-copied", "");
      clearTimeout(btn._docsCopied);
      btn._docsCopied = setTimeout(function () {
        btn.removeAttribute("data-copied");
      }, 2000);
    });
  });
  // 本页目录高亮页面顶部所在的小节。
  var docsSpyFrame = 0;
  function docsSpy() {
    docsSpyFrame = 0;
    var links = document.querySelectorAll("[data-docs-toc] a[href^='#']");
    var current = links[0];
    links.forEach(function (a) {
      var h = document.getElementById(a.getAttribute("href").slice(1));
      if (h && h.getBoundingClientRect().top <= 96) current = a;
    });
    links.forEach(function (a) {
      a.toggleAttribute("data-active", a === current);
    });
  }
  window.addEventListener(
    "scroll",
    function () {
      if (!docsSpyFrame) docsSpyFrame = requestAnimationFrame(docsSpy);
    },
    { passive: true },
  );
  // htmx 只替换正文区域：之后更新侧边栏的高亮，关闭手机上的侧边栏，回到页面顶部或链接中的小节。
  var docsPath = null;
  function docsNavigated() {
    var page = document.querySelector("[data-docs-path]");
    if (!page || page.getAttribute("data-docs-path") === docsPath) return;
    var first = docsPath === null;
    docsPath = page.getAttribute("data-docs-path");
    document.querySelectorAll("[data-docs-nav]").forEach(function (a) {
      a.toggleAttribute("data-active", a.getAttribute("href") === docsPath);
    });
    if (!first) {
      if (window.templ && window.templ.sidebar) window.templ.sidebar.setOpenMobile(false, "web-docs-sidebar");
      var hash = location.hash && document.getElementById(decodeURIComponent(location.hash.slice(1)));
      if (hash) hash.scrollIntoView();
      else window.scrollTo(0, 0);
    }
    docsSpy();
  }
  document.addEventListener("htmx:after:swap", docsNavigated);
  docsNavigated();

  // 扩展组件：页面出现选择器匹配的元素时加载对应脚本，每个只加载一次。
  var extras = JSON.parse((script && script.getAttribute("data-extras")) || "{}");
  function load() {
    Object.keys(extras).forEach(function (selector) {
      if (!document.querySelector(selector)) return;
      var s = document.createElement("script");
      s.src = extras[selector];
      if (script && script.nonce) s.nonce = script.nonce;
      document.head.appendChild(s);
      delete extras[selector];
    });
    if (Object.keys(extras).length === 0 && observer) observer.disconnect();
  }
  var observer = null;
  if (Object.keys(extras).length > 0) {
    observer = new MutationObserver(load);
    observer.observe(document.documentElement, { childList: true, subtree: true });
    load();
  }
})();
