// components/baseui/composite.js
// Port of @base-ui/react internals/composite (1.6.0): useCompositeRoot.ts,
// useCompositeItem.ts and composite.ts, with floating-ui-react's
// utils/composite.ts list helpers, which use_list_navigation.js shares. One tab stop for a group of items,
// the arrow keys move it by orientation, optionally Home and End, with loop.
//
//   const composite = window.templ.composite.useCompositeRoot(root, options)
//   composite.highlight(index)   onHighlightedIndexChange from the component
//   composite.index()            the highlighted index
//   composite.relayKeyboardEvent(event)   a key from a part portaled out of the root
//   composite.cleanup()
//
//   items()                the items in order (elementsRef)
//   loopFocus              default true
//   orientation            "horizontal", "vertical" or "both" (default)
//   rtl()                  the direction
//   enableHomeAndEndKeys   default false
//   stopEventPropagation   default true, CompositeRoot's
//   disabledIndices        an array, a function, or undefined for the DOM
//   modifierKeys           modifiers that do not cancel the navigation
//   highlightItemOnHover   default false, or a function read on every move
//   itemTabIndex           default true; false when the items render their
//                          own tabIndex over the composite one (NavigationMenu)
//   onHighlightedIndexChange(index)   after the highlight moved
//
// The root sets the default tab stop once on creation (onMapChange): the item
// with data-composite-item-active, or the first enabled one when the first
// is disabled. The items' tabIndex follows the highlight.
(function () {
  "use strict";

  const ACTIVE_COMPOSITE_ITEM = "data-composite-item-active";
  const ARROW_UP = "ArrowUp";
  const ARROW_DOWN = "ArrowDown";
  const ARROW_LEFT = "ArrowLeft";
  const ARROW_RIGHT = "ArrowRight";
  const HOME = "Home";
  const END = "End";
  const HORIZONTAL_KEYS = new Set([ARROW_LEFT, ARROW_RIGHT]);
  const HORIZONTAL_KEYS_WITH_EXTRA_KEYS = new Set([ARROW_LEFT, ARROW_RIGHT, HOME, END]);
  const VERTICAL_KEYS = new Set([ARROW_UP, ARROW_DOWN]);
  const VERTICAL_KEYS_WITH_EXTRA_KEYS = new Set([ARROW_UP, ARROW_DOWN, HOME, END]);
  const ARROW_KEYS = new Set([...HORIZONTAL_KEYS, ...VERTICAL_KEYS]);
  const COMPOSITE_KEYS = new Set([...ARROW_KEYS, HOME, END]);
  const MODIFIER_KEYS = new Set(["Shift", "Control", "Alt", "Meta"]);

  const t = () => window.templ.tabbable;

  // ----- utils/composite.ts: the list helpers, shared with use_list_navigation.js

  function isIndexOutOfListBounds(list, index) {
    return index < 0 || index >= list.length;
  }

  function isListIndexDisabled(list, index, disabledIndices) {
    const isExplicitlyDisabled = typeof disabledIndices === "function"
      ? disabledIndices(index)
      : disabledIndices?.includes(index) ?? false;
    if (isExplicitlyDisabled) return true;
    const element = list[index];
    if (!element) return false;
    if (!t().isElementVisible(element)) return true;
    return !disabledIndices && (element.hasAttribute("disabled") || element.getAttribute("aria-disabled") === "true");
  }

  function findNonDisabledListIndex(list, { startingIndex = -1, decrement = false, disabledIndices, amount = 1 } = {}) {
    let index = startingIndex;
    do {
      index += decrement ? -amount : amount;
    } while (index >= 0 && index <= list.length - 1 && isListIndexDisabled(list, index, disabledIndices));
    return index;
  }

  function getMinListIndex(list, disabledIndices) {
    return findNonDisabledListIndex(list, { disabledIndices });
  }

  function getMaxListIndex(list, disabledIndices) {
    return findNonDisabledListIndex(list, { decrement: true, startingIndex: list.length, disabledIndices });
  }

  const list = () => ({ isIndexOutOfListBounds, isListIndexDisabled, findNonDisabledListIndex, getMinListIndex, getMaxListIndex });

  function isNativeInput(element) {
    if (element instanceof HTMLElement && element.tagName === "INPUT" && element.selectionStart != null) return true;
    return element instanceof HTMLElement && element.tagName === "TEXTAREA";
  }

  function isElementDisabled(element) {
    return element == null || element.hasAttribute("disabled") || element.getAttribute("aria-disabled") === "true";
  }

  function getOffset(ancestor, element, side) {
    const propName = side === "left" ? "offsetLeft" : "offsetTop";
    let result = 0;
    while (element.offsetParent) {
      result += element[propName];
      if (element.offsetParent === ancestor) break;
      element = element.offsetParent;
    }
    return result;
  }

  function getStyles(element) {
    const styles = getComputedStyle(element);
    return {
      scrollMarginTop: parseFloat(styles.scrollMarginTop) || 0,
      scrollMarginRight: parseFloat(styles.scrollMarginRight) || 0,
      scrollMarginBottom: parseFloat(styles.scrollMarginBottom) || 0,
      scrollMarginLeft: parseFloat(styles.scrollMarginLeft) || 0,
      scrollPaddingTop: parseFloat(styles.scrollPaddingTop) || 0,
      scrollPaddingRight: parseFloat(styles.scrollPaddingRight) || 0,
      scrollPaddingBottom: parseFloat(styles.scrollPaddingBottom) || 0,
      scrollPaddingLeft: parseFloat(styles.scrollPaddingLeft) || 0,
    };
  }

  function scrollIntoViewIfNeeded(scrollContainer, element, direction, orientation) {
    if (!scrollContainer || !element || !element.scrollTo) return;
    let targetX = scrollContainer.scrollLeft;
    let targetY = scrollContainer.scrollTop;
    const isOverflowingX = scrollContainer.clientWidth < scrollContainer.scrollWidth;
    const isOverflowingY = scrollContainer.clientHeight < scrollContainer.scrollHeight;
    if (isOverflowingX && orientation !== "vertical") {
      const elementOffsetLeft = getOffset(scrollContainer, element, "left");
      const containerStyles = getStyles(scrollContainer);
      const elementStyles = getStyles(element);
      const overflowsRight = elementOffsetLeft + element.offsetWidth + elementStyles.scrollMarginRight >
        scrollContainer.scrollLeft + scrollContainer.clientWidth - containerStyles.scrollPaddingRight;
      const alignRight = elementOffsetLeft + element.offsetWidth + elementStyles.scrollMarginRight -
        scrollContainer.clientWidth + containerStyles.scrollPaddingRight;
      const alignLeft = elementOffsetLeft - elementStyles.scrollMarginLeft - containerStyles.scrollPaddingLeft;
      if (direction === "ltr") {
        if (overflowsRight) targetX = alignRight;
        else if (elementOffsetLeft - elementStyles.scrollMarginLeft < scrollContainer.scrollLeft + containerStyles.scrollPaddingLeft) targetX = alignLeft;
      }
      if (direction === "rtl") {
        if (elementOffsetLeft - elementStyles.scrollMarginRight < scrollContainer.scrollLeft + containerStyles.scrollPaddingLeft) targetX = alignLeft;
        else if (overflowsRight) targetX = alignRight;
      }
    }
    if (isOverflowingY && orientation !== "horizontal") {
      const elementOffsetTop = getOffset(scrollContainer, element, "top");
      const containerStyles = getStyles(scrollContainer);
      const elementStyles = getStyles(element);
      if (elementOffsetTop - elementStyles.scrollMarginTop < scrollContainer.scrollTop + containerStyles.scrollPaddingTop) {
        targetY = elementOffsetTop - elementStyles.scrollMarginTop - containerStyles.scrollPaddingTop;
      } else if (elementOffsetTop + element.offsetHeight + elementStyles.scrollMarginBottom >
        scrollContainer.scrollTop + scrollContainer.clientHeight - containerStyles.scrollPaddingBottom) {
        targetY = elementOffsetTop + element.offsetHeight + elementStyles.scrollMarginBottom -
          scrollContainer.clientHeight + containerStyles.scrollPaddingBottom;
      }
    }
    scrollContainer.scrollTo({ left: targetX, top: targetY, behavior: "auto" });
  }

  function isModifierKeySet(event, ignoredModifierKeys) {
    for (const key of MODIFIER_KEYS.values()) {
      if (ignoredModifierKeys.includes(key)) continue;
      if (event.getModifierState(key)) return true;
    }
    return false;
  }

  function useCompositeRoot(root, options) {
    const {
      items,
      loopFocus = true,
      orientation = "both",
      rtl = () => false,
      enableHomeAndEndKeys = false,
      stopEventPropagation = true,
      disabledIndices,
      modifierKeys = [],
      highlightItemOnHover = false,
      itemTabIndex = true,
      onHighlightedIndexChange: onChange,
    } = options;
    let highlightedIndex = 0;
    const direction = () => (rtl() ? "rtl" : "ltr");

    // useCompositeItem's tabIndex on every item.
    function applyTabIndex() {
      if (!itemTabIndex) return;
      items().forEach((item, index) => {
        item.tabIndex = index === highlightedIndex ? 0 : -1;
      });
    }

    function onHighlightedIndexChange(index, shouldScrollIntoView = false) {
      highlightedIndex = index;
      applyTabIndex();
      if (shouldScrollIntoView) scrollIntoViewIfNeeded(root, items()[index], direction(), orientation);
      onChange?.(index);
    }

    // onMapChange: the first population sets the default tab stop.
    const elements = items();
    const activeItem = elements.find((element) => element.hasAttribute(ACTIVE_COMPOSITE_ITEM)) ?? null;
    const activeIndex = activeItem ? elements.indexOf(activeItem) : -1;
    if (activeIndex !== -1) {
      highlightedIndex = activeIndex;
    } else if (list().isListIndexDisabled(elements, highlightedIndex, disabledIndices)) {
      // A disabled item does not hold the single tab stop: move it to the
      // first enabled one, or keep it when every item is disabled.
      const firstEnabledIndex = list().findNonDisabledListIndex(elements, { disabledIndices });
      if (!list().isIndexOutOfListBounds(elements, firstEnabledIndex)) highlightedIndex = firstEnabledIndex;
    }
    applyTabIndex();
    scrollIntoViewIfNeeded(root, activeItem, direction(), orientation);

    function onKeyDown(event) {
      const RELEVANT_KEYS = enableHomeAndEndKeys ? COMPOSITE_KEYS : ARROW_KEYS;
      if (!RELEVANT_KEYS.has(event.key)) return;
      if (isModifierKeySet(event, modifierKeys)) return;
      const elementsList = items();
      const isRtl = rtl();
      const horizontalForwardKey = isRtl ? ARROW_LEFT : ARROW_RIGHT;
      const horizontalBackwardKey = isRtl ? ARROW_RIGHT : ARROW_LEFT;
      const forwardKey = { horizontal: horizontalForwardKey, vertical: ARROW_DOWN, both: horizontalForwardKey }[orientation];
      const backwardKey = { horizontal: horizontalBackwardKey, vertical: ARROW_UP, both: horizontalBackwardKey }[orientation];
      const target = event.target;
      if (target != null && isNativeInput(target) && !isElementDisabled(target)) {
        const selectionStart = target.selectionStart;
        const selectionEnd = target.selectionEnd;
        const textContent = target.value ?? "";
        // Native textbox behavior: a text selection, arrowing forward before
        // the end, or backward after the start.
        if (selectionStart == null || event.shiftKey || selectionStart !== selectionEnd) return;
        if (event.key !== backwardKey && selectionStart < textContent.length) return;
        if (event.key !== forwardKey && selectionStart > 0) return;
      }
      let nextIndex = highlightedIndex;
      const minIndex = list().getMinListIndex(elementsList, disabledIndices);
      const maxIndex = list().getMaxListIndex(elementsList, disabledIndices);
      const forwardKeys = { horizontal: [horizontalForwardKey], vertical: [ARROW_DOWN], both: [horizontalForwardKey, ARROW_DOWN] }[orientation];
      const backwardKeys = { horizontal: [horizontalBackwardKey], vertical: [ARROW_UP], both: [horizontalBackwardKey, ARROW_UP] }[orientation];
      const preventedKeys = {
        horizontal: enableHomeAndEndKeys ? HORIZONTAL_KEYS_WITH_EXTRA_KEYS : HORIZONTAL_KEYS,
        vertical: enableHomeAndEndKeys ? VERTICAL_KEYS_WITH_EXTRA_KEYS : VERTICAL_KEYS,
        both: RELEVANT_KEYS,
      }[orientation];
      if (enableHomeAndEndKeys) {
        if (event.key === HOME) nextIndex = minIndex;
        else if (event.key === END) nextIndex = maxIndex;
      }
      if (nextIndex === highlightedIndex && (forwardKeys.includes(event.key) || backwardKeys.includes(event.key))) {
        if (loopFocus && nextIndex === maxIndex && forwardKeys.includes(event.key)) {
          nextIndex = minIndex;
        } else if (loopFocus && nextIndex === minIndex && backwardKeys.includes(event.key)) {
          nextIndex = maxIndex;
        } else {
          nextIndex = list().findNonDisabledListIndex(elementsList, {
            startingIndex: nextIndex,
            decrement: backwardKeys.includes(event.key),
            disabledIndices,
          });
        }
      }
      if (nextIndex !== highlightedIndex && !list().isIndexOutOfListBounds(elementsList, nextIndex)) {
        if (stopEventPropagation) event.stopPropagation();
        if (preventedKeys.has(event.key)) event.preventDefault();
        onHighlightedIndexChange(nextIndex, true);
        // After a focus manager's return focus.
        queueMicrotask(() => items()[nextIndex]?.focus());
      }
    }

    // The root's onFocus: a native input selects its text.
    function onFocus(event) {
      if (isNativeInput(event.target)) event.target.setSelectionRange(0, event.target.value.length ?? 0);
    }

    // useCompositeItem's onFocus and onMouseMove, delegated.
    function itemOf(target) {
      return items().find((item) => item === target || item.contains(target)) ?? null;
    }

    function onItemFocus(event) {
      const item = itemOf(event.target);
      if (item) onHighlightedIndexChange(items().indexOf(item));
    }

    function onItemMouseMove(event) {
      if (!(typeof highlightItemOnHover === "function" ? highlightItemOnHover() : highlightItemOnHover)) return;
      const item = itemOf(event.target);
      if (!item) return;
      const disabled = item.hasAttribute("disabled") || item.getAttribute("aria-disabled") === "true";
      if (items().indexOf(item) !== highlightedIndex && !disabled) item.focus();
    }

    root.addEventListener("keydown", onKeyDown);
    root.addEventListener("focusin", onFocus);
    root.addEventListener("focusin", onItemFocus);
    root.addEventListener("mousemove", onItemMouseMove);

    return {
      highlight: (index) => onHighlightedIndexChange(index),
      index: () => highlightedIndex,
      relayKeyboardEvent: onKeyDown,
      cleanup() {
        root.removeEventListener("keydown", onKeyDown);
        root.removeEventListener("focusin", onFocus);
        root.removeEventListener("focusin", onItemFocus);
        root.removeEventListener("mousemove", onItemMouseMove);
      },
    };
  }

  window.templ = window.templ || {};
  window.templ.composite = {
    useCompositeRoot,
    ACTIVE_COMPOSITE_ITEM,
    isIndexOutOfListBounds,
    isListIndexDisabled,
    findNonDisabledListIndex,
    getMinListIndex,
    getMaxListIndex,
  };
})();

// components/baseui/direction.js
// Port of @base-ui/react internals/direction-context (1.6.0): useDirection.
// The direction comes from the nearest DirectionProvider, "ltr" without
// one, never from CSS: a dir="rtl" attribute alone changes the layout, not
// Base UI's keyboard and positioning logic. DirectionProvider renders no
// element; the parts below one carry its value as data-templ-direction (a
// context React keeps in memory), and portaled parts reach it through their
// portal owner.
//
//   window.templ.direction.useDirection(element)   "ltr" or "rtl"
(function () {
  "use strict";

  function useDirection(element) {
    for (let node = element; node; node = window.templ.portal?.treeParent(node) ?? node.parentElement) {
      const direction = node.getAttribute?.("data-templ-direction");
      if (direction) return direction === "rtl" ? "rtl" : "ltr";
    }
    return "ltr";
  }

  window.templ = window.templ || {};
  window.templ.direction = { useDirection };
})();

// components/baseui/floating_focus_manager.js
// Port of @base-ui/react floating-ui-react/components/FloatingFocusManager.tsx
// (1.6.0), with utils/FocusGuard.tsx and floating-ui-react/utils/enqueueFocus.ts.
//
// The component's effects become one handle that follows the popup:
//
//   const focus = window.templ.focusManager.useFloatingFocusManager(options)  on mount
//   focus.close(details)   when it closes, details { reason, event }
//   focus.unmount()        once it unmounted after its exit animation
//
//   floating                  the floating element (positioner, or the popup itself)
//   reference                 the trigger, the source's domReference
//   triggers                  every trigger of the popup (a list), default [reference]
//   modal                     traps focus with guards and hides the outside (default true)
//   initialFocus              true, false, an element, or fn(interactionType)
//   returnFocus               true, false, an element, or fn(closeType)
//   restoreFocus              false, true, or "popup"
//   closeOnFocusOut           default true
//   openInteractionType       "mouse", "touch", "pen", "keyboard", "", or null (programmatic)
//   previousFocusableElement, nextFocusableElement
//   getInsideElements         fn returning elements that count as inside
//   onOpenChange(open, reason, event)   requests the close
//
// The floating tree is the portal owner chain, like in use_dismiss.js: a
// child is an open focus manager whose floating element lies inside this one.
// The portal context is the [data-base-ui-portal] node around the floating
// element, see portal.js.
(function () {
  "use strict";

  const t = () => window.templ.tabbable;
  const webkit = typeof CSS !== "undefined" && !!CSS.supports?.("-webkit-backdrop-filter:none");
  const FOCUSABLE_ATTRIBUTE = "data-base-ui-focusable";
  const CLICK_TRIGGER_IDENTIFIER = "data-base-ui-click-trigger";
  const TYPEABLE_SELECTOR = "input:not([type='hidden']):not([disabled]),[contenteditable]:not([contenteditable='false']),textarea:not([disabled])";

  const instances = new Set();

  // ----- enqueueFocus -------------------------------------------------------

  // Returns a cancel for the queued focus.
  let rafId = 0;
  function enqueueFocus(el, options = {}) {
    const { preventScroll = false, sync = false, shouldFocus } = options;
    cancelAnimationFrame(rafId);
    const exec = () => {
      if (shouldFocus && !shouldFocus()) return;
      el?.focus({ preventScroll });
    };
    if (sync) {
      exec();
      return () => {};
    }
    const currentRafId = requestAnimationFrame(exec);
    rafId = currentRafId;
    return () => {
      if (rafId === currentRafId) {
        cancelAnimationFrame(currentRafId);
        rafId = 0;
      }
    };
  }

  // ----- FocusGuard -----------------------------------------------------------

  // A visually hidden tabbable span that hands focus on when it receives it.
  // @base-ui/utils/platform: VoiceOver may run on any Apple OS. Through
  // WebKit its virtual cursor only focuses focusable or button elements, so
  // the guards there are buttons the cursor can land on instead of hidden.
  const platform = (navigator.platform || "").toLowerCase();
  const ios = /^i(os$|p)/.test(platform) || (platform === "macintel" && navigator.maxTouchPoints > 1);
  const apple = ios || platform.startsWith("mac");
  const voiceOverGuards = apple && webkit;

  function createFocusGuard(type, onFocus) {
    const guard = document.createElement("span");
    if (type) guard.setAttribute("data-type", type);
    if (voiceOverGuards) guard.setAttribute("role", "button");
    else guard.setAttribute("aria-hidden", "true");
    guard.setAttribute("tabindex", "0");
    guard.setAttribute("data-base-ui-focus-guard", "");
    guard.style.cssText = "clip-path:inset(50%);overflow:hidden;white-space:nowrap;border:0;padding:0;width:1px;height:1px;margin:-1px;position:fixed;top:0;left:0";
    if (onFocus) guard.addEventListener("focus", onFocus);
    return guard;
  }

  // ----- helpers from the source's utils --------------------------------------

  const getTarget = (event) => event.composedPath?.()[0] || event.target;
  const isHTMLElement = (node) => node instanceof HTMLElement;
  const isTypeableElement = (element) => isHTMLElement(element) && element.matches(TYPEABLE_SELECTOR);
  const isTypeableCombobox = (element) => !!element && element.getAttribute("role") === "combobox" && isTypeableElement(element);
  const stopEvent = (event) => {
    event.preventDefault();
    event.stopPropagation();
  };

  // The element that takes focus: the one marked focusable inside a
  // positioning wrapper, or the floating element itself.
  function getFloatingFocusElement(floatingElement) {
    if (!floatingElement) return null;
    return floatingElement.hasAttribute(FOCUSABLE_ATTRIBUTE)
      ? floatingElement
      : floatingElement.querySelector(`[${FOCUSABLE_ATTRIBUTE}]`) || floatingElement;
  }

  function getFirstTabbableElement(container) {
    if (!container) return null;
    if (t().isTabbable(container)) return container;
    return t().tabbable(container)[0] || container;
  }

  // The React tree pendant, see use_dismiss.js.
  function withinTree(root, target) {
    for (let node = target; node; node = window.templ.portal.treeParent(node)) {
      if (node === root) return true;
    }
    return false;
  }

  function isVirtualClick(event) {
    if (event.pointerType === "" && event.isTrusted) return true;
    return event.detail === 0 && !event.pointerType;
  }

  function isVirtualPointerEvent(event) {
    return (event.width === 0 && event.height === 0) ||
      (event.width < 1 && event.height < 1 && event.pressure === 0 && event.detail === 0 && event.pointerType === "touch");
  }

  function getEventType(event, lastInteractionType) {
    if (!event) return lastInteractionType || "";
    if (event instanceof KeyboardEvent) return "keyboard";
    if (event instanceof FocusEvent) return lastInteractionType || "keyboard";
    if ("pointerType" in event) return event.pointerType || "keyboard";
    if ("touches" in event) return "touch";
    if (event instanceof MouseEvent) return lastInteractionType || (event.detail === 0 ? "keyboard" : "mouse");
    return "";
  }

  // The elements focused before a modal popup took focus, most recent last.
  const LIST_LIMIT = 20;
  let previouslyFocusedElements = [];
  function clearDisconnectedPreviouslyFocusedElements() {
    previouslyFocusedElements = previouslyFocusedElements.filter((entry) => entry.deref()?.isConnected);
  }
  function addPreviouslyFocusedElement(element) {
    clearDisconnectedPreviouslyFocusedElements();
    if (element && element.nodeName.toLowerCase() !== "body") {
      previouslyFocusedElements.push(new WeakRef(element));
      if (previouslyFocusedElements.length > LIST_LIMIT) previouslyFocusedElements = previouslyFocusedElements.slice(-LIST_LIMIT);
    }
  }
  function getPreviouslyFocusedElement() {
    clearDisconnectedPreviouslyFocusedElements();
    return previouslyFocusedElements[previouslyFocusedElements.length - 1]?.deref();
  }

  // A dialog without tabbable content takes the Tab stop itself. data-tabindex
  // marks the value Base UI wrote, so a user authored tabindex is left alone.
  function handleTabIndex(floatingFocusElement) {
    if (floatingFocusElement.hasAttribute("tabindex") && !floatingFocusElement.hasAttribute("data-tabindex")) return;
    if (!floatingFocusElement.getAttribute("role")?.includes("dialog")) return;
    const tabbableContent = t().focusable(floatingFocusElement).filter((element) => {
      const dataTabIndex = element.getAttribute("data-tabindex") || "";
      return t().isTabbable(element) || (element.hasAttribute("data-tabindex") && !dataTabIndex.startsWith("-"));
    });
    const tabIndex = floatingFocusElement.getAttribute("tabindex");
    if (tabbableContent.length === 0) {
      if (tabIndex !== "0") {
        floatingFocusElement.setAttribute("tabindex", "0");
        floatingFocusElement.setAttribute("data-tabindex", "0");
      }
    } else if (tabIndex !== "-1" || (floatingFocusElement.hasAttribute("data-tabindex") && floatingFocusElement.getAttribute("data-tabindex") !== "-1")) {
      floatingFocusElement.setAttribute("tabindex", "-1");
      floatingFocusElement.setAttribute("data-tabindex", "-1");
    }
  }

  // ----- the focus manager ------------------------------------------------------

  function useFloatingFocusManager(options) {
    const {
      floating,
      reference: domReference = null,
      modal = true,
      initialFocus = true,
      returnFocus = true,
      restoreFocus = false,
      closeOnFocusOut = true,
      previousFocusableElement = null,
      nextFocusableElement = null,
      getInsideElements,
      onOpenChange,
      // The element the guards wrap: the focus manager's children, the
      // floating focus element unless the popup holds a focusable list.
      guardsAround = null,
    } = options;
    // A reopened popup renders the manager again with this open's type.
    let openInteractionType = options.openInteractionType === undefined ? "" : options.openInteractionType;
    const triggers = [...(options.triggers || [domReference])].filter(Boolean);
    const doc = floating.ownerDocument;
    const floatingFocusElement = getFloatingFocusElement(floating);
    const portalNode = floating.closest("[data-base-ui-portal]");
    const ignoreInitialFocus = initialFocus === false;
    // A typeable combobox reference keeps focus in its input: no guards,
    // but the outside is still hidden.
    const isUntrappedTypeableCombobox = isTypeableCombobox(domReference) && ignoreInitialFocus;
    const self = { floating, reference: domReference, open: false };
    const cleanups = [];
    const openCleanups = [];

    let preventReturnFocus = false;
    let isPointerDown = false;
    let pointerDownOutside = false;
    let lastFocusedTabbable = null;
    let closeType = "";
    let lastInteractionType = "";
    let insideReactTree = false;
    let blurTimer = 0;
    let pointerDownTimer = 0;

    const getTabbableContent = (container = floatingFocusElement) => (container ? t().tabbable(container) : []);
    const getResolvedInsideElements = () => getInsideElements?.().filter((element) => element != null) ?? [];
    const on = (list, target, type, listener, capture) => {
      target.addEventListener(type, listener, !!capture);
      list.push(() => target.removeEventListener(type, listener, !!capture));
    };
    const isChild = (other) => other !== self && withinTree(floating, other.floating);
    const isAncestor = (other) => other !== self && withinTree(other.floating, floating);

    // Guards inside the floating tree: a modal trap, or for a non modal popup
    // in a portal the way back out through the portal's outside guards.
    const shouldRenderGuards = (modal ? !isUntrappedTypeableCombobox : true) && (portalNode != null || modal);
    let beforeGuard = null;
    let afterGuard = null;
    if (shouldRenderGuards) {
      beforeGuard = createFocusGuard("inside", (event) => {
        if (modal) {
          const els = getTabbableContent();
          enqueueFocus(els[els.length - 1]);
        } else if (portalNode) {
          preventReturnFocus = false;
          if (t().isOutsideEvent(event, portalNode)) {
            t().getNextTabbable(domReference)?.focus();
          } else {
            (previousFocusableElement || portalNode._templPortalGuards?.beforeOutside)?.focus();
          }
        }
      });
      afterGuard = createFocusGuard("inside", (event) => {
        if (modal) {
          enqueueFocus(getTabbableContent()[0]);
        } else if (portalNode) {
          if (closeOnFocusOut) preventReturnFocus = true;
          if (t().isOutsideEvent(event, portalNode)) {
            t().getPreviousTabbable(domReference)?.focus();
          } else {
            (nextFocusableElement || portalNode._templPortalGuards?.afterOutside)?.focus();
          }
        }
      });
      (guardsAround || floatingFocusElement).before(beforeGuard);
      (guardsAround || floatingFocusElement).after(afterGuard);
    }

    // Prevent Tab from escaping the modal when there is nothing tabbable.
    if (modal) {
      on(cleanups, doc, "keydown", (event) => {
        if (event.key === "Tab" && t().contains(floatingFocusElement, t().activeElement(doc)) &&
          getTabbableContent().length === 0 && !isUntrappedTypeableCombobox) {
          stopEvent(event);
        }
      });
    }

    // Close on focus out, and restore focus inside the floating tree.
    if (closeOnFocusOut) {
      const handlePointerDown = () => {
        isPointerDown = true;
        clearTimeout(pointerDownTimer);
        pointerDownTimer = setTimeout(() => {
          isPointerDown = false;
        }, 0);
      };
      const handleFocusIn = (event) => {
        const target = getTarget(event);
        if (t().isTabbable(target)) lastFocusedTabbable = target;
      };
      const handleFocusOutside = (event) => {
        const relatedTarget = event.relatedTarget;
        const currentTarget = event.currentTarget;
        const target = getTarget(event);
        // Focus lost to the body (a backdrop press): remember what had it, so a
        // confirmation dialog opened then can return focus there.
        if (modal && relatedTarget == null && target != null && t().contains(floating, target)) {
          addPreviouslyFocusedElement(target);
        }
        queueMicrotask(() => {
          const insideElements = getResolvedInsideElements();
          const portalGuards = portalNode?._templPortalGuards;
          const isRelatedFocusGuard = relatedTarget?.hasAttribute?.("data-base-ui-focus-guard") &&
            [beforeGuard, afterGuard, portalGuards?.beforeOutside, portalGuards?.afterOutside, previousFocusableElement, nextFocusableElement].includes(relatedTarget);
          const insideChild = [...instances].some((other) => isChild(other) && t().contains(other.floating, relatedTarget));
          // An ancestor's popup or its reference, not the ancestor's items.
          const toAncestor = [...instances].some((other) => isAncestor(other) &&
            ([other.floating, getFloatingFocusElement(other.floating)].includes(relatedTarget) || other.reference === relatedTarget));
          const movedToUnrelatedNode = !(
            t().contains(domReference, relatedTarget) ||
            withinTree(floating, relatedTarget) ||
            t().contains(relatedTarget, floating) ||
            t().contains(portalNode, relatedTarget) ||
            insideElements.some((element) => element === relatedTarget || t().contains(element, relatedTarget)) ||
            (relatedTarget != null && triggers.includes(relatedTarget)) ||
            triggers.some((trigger) => t().contains(trigger, relatedTarget)) ||
            isRelatedFocusGuard ||
            insideChild ||
            toAncestor
          );
          if (currentTarget === domReference && floatingFocusElement) handleTabIndex(floatingFocusElement);

          // Focus lost outside the floating tree when its element went away.
          if (restoreFocus && currentTarget !== domReference && !t().isElementVisible(target) && t().activeElement(doc) === doc.body) {
            if (isHTMLElement(floatingFocusElement)) {
              floatingFocusElement.focus();
              if (restoreFocus === "popup") {
                requestAnimationFrame(() => floatingFocusElement.focus());
                return;
              }
            }
            const tabbableContent = getTabbableContent();
            const prevTabbable = lastFocusedTabbable;
            const nodeToFocus = (prevTabbable && tabbableContent.includes(prevTabbable) ? prevTabbable : null) ||
              tabbableContent[tabbableContent.length - 1] || floatingFocusElement;
            if (isHTMLElement(nodeToFocus)) nodeToFocus.focus();
          }

          if (insideReactTree) {
            insideReactTree = false;
            return;
          }

          // Focus moved out of the floating tree with no portal guard to handle it.
          if ((isUntrappedTypeableCombobox ? true : !modal) && relatedTarget && movedToUnrelatedNode && !isPointerDown &&
            (isUntrappedTypeableCombobox || relatedTarget !== getPreviouslyFocusedElement())) {
            preventReturnFocus = true;
            onOpenChange?.(false, "focus-out", event);
          }
        });
      };
      // Focus leaving through a portaled child still counts as inside.
      const markInsideReactTree = () => {
        if (pointerDownOutside) return;
        insideReactTree = true;
        clearTimeout(blurTimer);
        blurTimer = setTimeout(() => {
          insideReactTree = false;
        }, 0);
      };
      if (isHTMLElement(domReference)) {
        on(cleanups, domReference, "focusout", handleFocusOutside);
        on(cleanups, domReference, "pointerdown", handlePointerDown);
      }
      on(cleanups, floating, "focusin", handleFocusIn);
      on(cleanups, floating, "focusout", handleFocusOutside);
      if (portalNode) on(cleanups, floating, "focusout", markInsideReactTree, true);
    }

    // The effects that run while open. A popup that opens again during its
    // exit animation runs them again on the same manager.
    function open(nextOpenInteractionType) {
      if (nextOpenInteractionType !== undefined) openInteractionType = nextOpenInteractionType;
      self.open = true;
      // The portal's outside guards render first, they count as inside below.
      window.templ.portal.setFocusManagerState(portalNode, { ...portalNode?._templFocusState, open: true });
      // Track pointer and keyboard interactions while open.
      on(openCleanups, doc, "pointerdown", (event) => {
        const target = getTarget(event);
        const insideElements = getResolvedInsideElements();
        const pointerTargetInside = t().contains(floating, target) || t().contains(domReference, target) ||
          t().contains(portalNode, target) || insideElements.some((element) => element === target || t().contains(element, target));
        pointerDownOutside = !pointerTargetInside;
        lastInteractionType = event.pointerType || "keyboard";
        if (target?.closest?.(`[${CLICK_TRIGGER_IDENTIFIER}]`)) {
          isPointerDown = true;
          clearTimeout(pointerDownTimer);
          pointerDownTimer = setTimeout(() => {
            isPointerDown = false;
          }, 0);
        }
      }, true);
      const clearPointerDownOutside = () => {
        pointerDownOutside = false;
      };
      on(openCleanups, doc, "pointerup", clearPointerDownOutside, true);
      on(openCleanups, doc, "pointercancel", clearPointerDownOutside, true);
      on(openCleanups, doc, "keydown", () => {
        lastInteractionType = "keyboard";
      }, true);
      openCleanups.push(clearPointerDownOutside);

      // Hide everything outside the floating tree from assistive tech while open.
      const nestedPortalNodes = Array.from(portalNode?.querySelectorAll("[data-base-ui-portal]") || []);
      const portalGuards = portalNode?._templPortalGuards;
      const insideElements = [
        floating, ...nestedPortalNodes, beforeGuard, afterGuard, portalGuards?.beforeOutside, portalGuards?.afterOutside,
        ...getResolvedInsideElements(), previousFocusableElement, nextFocusableElement,
        isUntrappedTypeableCombobox ? domReference : null,
      ].filter((x) => x != null);
      const ariaHiddenCleanup = window.templ.markOthers(insideElements, { ariaHidden: modal || isUntrappedTypeableCombobox, mark: false });
      const markerCleanup = window.templ.markOthers([floating, ...nestedPortalNodes].filter((x) => x != null));
      openCleanups.push(() => {
        markerCleanup();
        ariaHiddenCleanup();
      });

      // Focus the initial element.
      const previouslyFocusedElement = t().activeElement(doc);
      queueMicrotask(() => {
        const resolvedInitialFocus = typeof initialFocus === "function" ? initialFocus(openInteractionType || "") : initialFocus;
        if (resolvedInitialFocus === undefined || resolvedInitialFocus === false) return;
        if (t().contains(floatingFocusElement, previouslyFocusedElement)) return;
        const getDefaultFocusElement = () => getTabbableContent(floatingFocusElement)[0] || floatingFocusElement;
        let elToFocus = resolvedInitialFocus === true || resolvedInitialFocus === null ? getDefaultFocusElement() : resolvedInitialFocus;
        elToFocus = elToFocus || getDefaultFocusElement();
        const hadFocusInside = t().contains(floatingFocusElement, t().activeElement(doc));
        enqueueFocus(elToFocus, {
          preventScroll: elToFocus === floatingFocusElement,
          shouldFocus() {
            if (!self.open) return false;
            if (hadFocusInside) return true;
            const currentActiveElement = t().activeElement(doc);
            return !(currentActiveElement !== elToFocus && t().contains(floatingFocusElement, currentActiveElement));
          },
        });
      });
    }

    // Return focus targets, restored on unmount. Only a null interaction
    // type is a programmatic open.
    const elementFocusedBeforeOpen = t().activeElement(doc);
    const preferPreviousFocus = openInteractionType == null;
    addPreviouslyFocusedElement(elementFocusedBeforeOpen);

    function getReturnElement() {
      let resolved = typeof returnFocus === "function" ? returnFocus(closeType) : returnFocus;
      if (resolved === undefined || resolved === false) return null;
      if (resolved === null) resolved = true;
      const referenceReturnElement = domReference?.isConnected ? domReference : null;
      const previousReturnElement = elementFocusedBeforeOpen?.isConnected && elementFocusedBeforeOpen.nodeName.toLowerCase() !== "body"
        ? elementFocusedBeforeOpen
        : null;
      let defaultReturnElement = preferPreviousFocus
        ? previousReturnElement || referenceReturnElement
        : referenceReturnElement || previousReturnElement;
      if (!defaultReturnElement) defaultReturnElement = getPreviouslyFocusedElement() || null;
      if (typeof resolved === "boolean") return defaultReturnElement;
      return resolved || defaultReturnElement || null;
    }

    // The portal renders its outside guards for a non modal manager.
    window.templ.portal.setFocusManagerState(portalNode, {
      modal,
      closeOnFocusOut,
      open: false,
      onOpenChange,
      domReference,
      beforeInside: beforeGuard,
      afterInside: afterGuard,
    });
    if (floatingFocusElement) handleTabIndex(floatingFocusElement);
    instances.add(self);
    open();

    function close(details = {}) {
      if (!self.open) return;
      self.open = false;
      closeType = getEventType(details.event, lastInteractionType);
      if (details.reason === "trigger-hover" && details.event?.type === "mouseleave") preventReturnFocus = true;
      if (details.reason === "outside-press") {
        // An outside press returns focus without scrolling where the browser
        // supports preventScroll, everywhere current.
        const event = details.event;
        if (details.nested || (event && (isVirtualClick(event) || isVirtualPointerEvent(event)))) {
          preventReturnFocus = false;
        } else {
          let isPreventScrollSupported = false;
          doc.createElement("div").focus({
            get preventScroll() {
              isPreventScrollSupported = true;
              return false;
            },
          });
          preventReturnFocus = !isPreventScrollSupported;
        }
      }
      openCleanups.splice(0).forEach((cleanup) => cleanup());
      window.templ.portal.setFocusManagerState(portalNode, { ...portalNode?._templFocusState, open: false });
      // Safari may scroll to the bottom when an input inside the popup keeps
      // focus while it unmounts.
      const activeEl = t().activeElement(doc);
      if (webkit && isHTMLElement(activeEl) && isTypeableElement(activeEl) && t().contains(floating, activeEl)) activeEl.blur();
    }

    function unmount() {
      if (self.open) close();
      instances.delete(self);
      cleanups.splice(0).forEach((cleanup) => cleanup());
      clearTimeout(blurTimer);
      clearTimeout(pointerDownTimer);
      window.templ.portal.setFocusManagerState(portalNode, null);
      beforeGuard?.remove();
      afterGuard?.remove();

      const activeEl = t().activeElement(doc);
      const isFocusInsideFloatingTree = t().contains(floating, activeEl) ||
        getResolvedInsideElements().some((element) => element === activeEl || t().contains(element, activeEl)) ||
        [...instances].some((other) => isChild(other) && t().contains(other.floating, activeEl));
      const returnElement = getReturnElement();
      queueMicrotask(() => {
        const tabbableReturnElement = getFirstTabbableElement(returnElement);
        const hasExplicitReturnFocus = typeof returnFocus !== "boolean";
        // Focus that moved elsewhere after mount is respected.
        if (returnFocus && !preventReturnFocus && isHTMLElement(tabbableReturnElement) &&
          (!hasExplicitReturnFocus && tabbableReturnElement !== activeEl && activeEl !== doc.body ? isFocusInsideFloatingTree : true)) {
          tabbableReturnElement.focus({ preventScroll: true });
        }
        preventReturnFocus = false;
        clearDisconnectedPreviouslyFocusedElements();
      });
    }

    // beforeGuard is the source's beforeContentFocusGuardRef.
    return { open, close, unmount, beforeGuard };
  }

  window.templ = window.templ || {};
  window.templ.focusManager = { useFloatingFocusManager, createFocusGuard, enqueueFocus };
})();

// components/baseui/internal_backdrop.js
// Port of @base-ui/react utils/InternalBackdrop.tsx (1.6.0), the backdrop a
// modal menu's or select's positioner renders while mounted: fixed over the
// viewport, before the positioner, inert while closing, with a hole over the
// cutout (the trigger) so it stays pressable. useDismiss treats it as an
// outside element (data-base-ui-inert).
(function () {
  "use strict";

  function mount(positioner, cutout) {
    remove(positioner);
    const backdrop = document.createElement("div");
    backdrop.setAttribute("role", "presentation");
    backdrop.setAttribute("data-base-ui-inert", "");
    let clipPath = "";
    if (cutout) {
      const r = cutout.getBoundingClientRect();
      clipPath = "clip-path:polygon(0% 0%,100% 0%,100% 100%,0% 100%,0% 0%," +
        `${r.left}px ${r.top}px,${r.left}px ${r.bottom}px,${r.right}px ${r.bottom}px,` +
        `${r.right}px ${r.top}px,${r.left}px ${r.top}px)`;
    }
    backdrop.style.cssText = "position:fixed;inset:0;user-select:none;-webkit-user-select:none;" + clipPath;
    positioner.before(backdrop);
    positioner._templBackdrop = backdrop;
  }

  // inertValue(!open): the backdrop takes no input once the close started.
  function inert(positioner) {
    if (positioner._templBackdrop) positioner._templBackdrop.inert = true;
  }

  function remove(positioner) {
    positioner._templBackdrop?.remove();
    positioner._templBackdrop = null;
  }

  window.templ = window.templ || {};
  window.templ.internalBackdrop = { mount, inert, remove };
})();

// components/baseui/lifecycle.js
// Pendant of React mount and unmount. A Base UI part mounts when React renders
// it and unmounts when React removes it, and a portaled part stays mounted as
// long as the element that rendered it. Here one MutationObserver does that
// for every component: init(el) runs once when an element matching the
// selector appears, destroy(el) once when it is gone.
(function () {
  "use strict";

  const components = [];

  // A portaled subtree lives as long as its declaration site (portal owner),
  // like React unmounts a portal with the component that rendered it.
  function mounted(el) {
    if (!el.isConnected) return false;
    for (let node = el; node; node = node.parentElement) {
      if (node._templPortalOwner && !mounted(node._templPortalOwner)) return false;
    }
    return true;
  }

  function sync(component) {
    for (const el of component.elements) {
      if (mounted(el)) continue;
      component.elements.delete(el);
      component.destroy?.(el);
    }
    for (const el of document.querySelectorAll(component.selector)) {
      if (component.elements.has(el) || !mounted(el)) continue;
      component.elements.add(el);
      component.init?.(el);
    }
  }

  // register(selector, { init, destroy }) wires every matching element that
  // is in the document now and every one that appears later.
  function register(selector, { init, destroy } = {}) {
    const component = { selector, init, destroy, elements: new Set() };
    components.push(component);
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", () => sync(component));
    else sync(component);
  }

  new MutationObserver(() => components.forEach(sync))
    .observe(document.documentElement, { childList: true, subtree: true });

  window.templ = window.templ || {};
  window.templ.lifecycle = { register };
})();

// components/baseui/mark_others.js
// Port of @base-ui/react floating-ui-react/utils/markOthers.ts (1.6.0), itself a
// fork of aria-hidden with a conditional aria-hidden. markOthers(avoid, options)
// marks every element outside the avoided subtrees and returns the undo.
//
//   ariaHidden  sets aria-hidden="true" outside (modal popups)
//   inert       sets inert outside
//   mark        sets the data-base-ui-inert marker outside, which useDismiss
//               reads to tell elements injected after opening (default true)
//
// Counted per element, so nested popups and several open ones undo cleanly,
// and elements that were hidden before are left as they were.
(function () {
  "use strict";

  const markerName = "data-base-ui-inert";
  let counters = { inert: new WeakMap(), "aria-hidden": new WeakMap() };
  let uncontrolledElementsSets = { inert: new WeakSet(), "aria-hidden": new WeakSet() };
  let markerCounterMap = new WeakMap();
  let lockCount = 0;

  const isShadowRoot = (node) => typeof ShadowRoot !== "undefined" && node instanceof ShadowRoot;

  function unwrapHost(node) {
    if (!node) return null;
    return isShadowRoot(node) ? node.host : unwrapHost(node.parentNode);
  }

  function correctElements(parent, targets) {
    return targets.map((target) => {
      if (parent.contains(target)) return target;
      const correctedTarget = unwrapHost(target);
      if (parent.contains(correctedTarget)) return correctedTarget;
      return null;
    }).filter((x) => x != null);
  }

  function buildKeepSet(targets) {
    const keep = new Set();
    targets.forEach((target) => {
      let node = target;
      while (node && !keep.has(node)) {
        keep.add(node);
        node = node.parentNode;
      }
    });
    return keep;
  }

  function collectOutsideElements(root, keepElements, stopElements) {
    const outside = [];
    const walk = (parent) => {
      if (!parent || stopElements.has(parent)) return;
      Array.from(parent.children).forEach((node) => {
        if (node.nodeName.toLowerCase() === "script") return;
        if (keepElements.has(node)) walk(node);
        else outside.push(node);
      });
    };
    walk(root);
    return outside;
  }

  function applyAttributeToOthers(uncorrectedAvoidElements, body, ariaHidden, inert, { mark = true }) {
    let controlAttribute = null;
    if (inert) controlAttribute = "inert";
    else if (ariaHidden) controlAttribute = "aria-hidden";
    let counterMap = null;
    let uncontrolledElementsSet = null;
    const avoidElements = correctElements(body, uncorrectedAvoidElements);
    const markerTargets = mark ? collectOutsideElements(body, buildKeepSet(avoidElements), new Set(avoidElements)) : [];
    const hiddenElements = [];
    const markedElements = [];
    if (controlAttribute) {
      const map = counters[controlAttribute];
      uncontrolledElementsSet = uncontrolledElementsSets[controlAttribute];
      counterMap = map;
      const ariaLiveElements = correctElements(body, Array.from(body.querySelectorAll("[aria-live]")));
      const controlElements = avoidElements.concat(ariaLiveElements);
      const controlTargets = collectOutsideElements(body, buildKeepSet(controlElements), new Set(controlElements));
      controlTargets.forEach((node) => {
        const attr = node.getAttribute(controlAttribute);
        const alreadyHidden = attr !== null && attr !== "false";
        const counterValue = (map.get(node) || 0) + 1;
        map.set(node, counterValue);
        hiddenElements.push(node);
        if (counterValue === 1 && alreadyHidden) uncontrolledElementsSet.add(node);
        if (!alreadyHidden) node.setAttribute(controlAttribute, controlAttribute === "inert" ? "" : "true");
      });
    }
    if (mark) {
      markerTargets.forEach((node) => {
        const markerValue = (markerCounterMap.get(node) || 0) + 1;
        markerCounterMap.set(node, markerValue);
        markedElements.push(node);
        if (markerValue === 1) node.setAttribute(markerName, "");
      });
    }
    lockCount += 1;
    return () => {
      if (counterMap) {
        hiddenElements.forEach((element) => {
          const counterValue = (counterMap.get(element) || 0) - 1;
          counterMap.set(element, counterValue);
          if (!counterValue) {
            if (!uncontrolledElementsSet?.has(element) && controlAttribute) element.removeAttribute(controlAttribute);
            uncontrolledElementsSet?.delete(element);
          }
        });
      }
      if (mark) {
        markedElements.forEach((element) => {
          const markerValue = (markerCounterMap.get(element) || 0) - 1;
          markerCounterMap.set(element, markerValue);
          if (!markerValue) element.removeAttribute(markerName);
        });
      }
      lockCount -= 1;
      if (!lockCount) {
        counters = { inert: new WeakMap(), "aria-hidden": new WeakMap() };
        uncontrolledElementsSets = { inert: new WeakSet(), "aria-hidden": new WeakSet() };
        markerCounterMap = new WeakMap();
      }
    };
  }

  function markOthers(avoidElements, options = {}) {
    const { ariaHidden = false, inert = false, mark = true } = options;
    const body = avoidElements[0].ownerDocument.body;
    return applyAttributeToOthers(avoidElements, body, ariaHidden, inert, { mark });
  }

  window.templ = window.templ || {};
  window.templ.markOthers = markOthers;
})();

// components/baseui/menu.js
// Port of what @base-ui/react menu (1.6.0) shares between shadcn's dropdown
// menu and context menu: MenuRoot's list navigation and typeahead,
// MenuPositioner's internal backdrop, MenuPopup's state attributes, the items
// (useMenuItem with useButton), groups, and SubmenuRoot with
// SubmenuTrigger. A component script creates its menu from its slot names and
// keeps its own trigger and root open: MenuTrigger for the dropdown menu,
// ContextMenu.Trigger for the context menu.
//
// A menu's element is its positioner (no slot upstream) around the
// [data-slot=<slot>-content] popup, inside the portal node. A submenu is its
// trigger: SubmenuRoot renders no element, the trigger links its popup in
// data-templ-controls.
(function () {
  "use strict";

  const ITEM_SELECTOR = '[role="menuitem"], [role="menuitemcheckbox"], [role="menuitemradio"]';

  // MenuSubmenuTrigger's defaults: hover opens after 100 ms at rest, the
  // close has no delay.
  const SUBMENU_DELAY = 100;
  const SUBMENU_CLOSE_DELAY = 0;

  // config: slot ("dropdown-menu"), event (the custom event prefix,
  // "dropdownmenu"), subPositioning (the submenu's MenuPositioner props),
  // onItemPress(positioner, event) closes the root menu.
  function create(config) {
    const { slot, event: eventPrefix, subPositioning, onItemPress } = config;
    const POPUP = '[data-slot="' + slot + '-content"]';
    const SUB_TRIGGER = '[data-slot="' + slot + '-sub-trigger"]';
    const SUB_CONTENT = '[data-slot="' + slot + '-sub-content"]';
    const GROUP = '[data-slot="' + slot + '-group"], [data-slot="' + slot + '-radio-group"]';
    const LABEL = '[data-slot="' + slot + '-label"]';

    // ----- parts ---------------------------------------------------------------

    function isPositioner(el) {
      return !!el?.querySelector?.(":scope > " + POPUP);
    }

    function popupFor(positioner) {
      return positioner.querySelector(":scope > " + POPUP);
    }

    // The root positioner of a popup id, or null.
    function positionerById(id) {
      const popup = id && document.getElementById(id);
      return popup?.matches(POPUP) && isPositioner(popup.parentElement) ? popup.parentElement : null;
    }

    function allPositioners() {
      return [...document.querySelectorAll(POPUP)].map((p) => p.parentElement).filter(isPositioner);
    }

    // The root positioner of the menu an element sits in, portaled submenus
    // included.
    function positionerOf(target) {
      for (let node = target; node; node = window.templ.portal.treeParent(node)) {
        if (node.matches?.(POPUP) && isPositioner(node.parentElement)) return node.parentElement;
      }
      return null;
    }

    function isOpen(positioner) {
      return !!positioner && positioner.hasAttribute("data-open");
    }

    // The positioner's parent is the portal node, which moves to <body> on
    // open and is unmounted while the menu is (MenuPortal).
    function portalNodeOf(positioner) {
      return positioner.parentElement;
    }

    // An explicit Portal part (shadcn's <slot>-portal) around a submenu's
    // content mounts with it, the content's own portal node inside it.
    function outerPortalOf(positioner) {
      if (positioner._templOuterPortal === undefined) {
        const outer = portalNodeOf(positioner).parentElement
          ?.closest('[data-slot="' + slot + '-portal"], ' + SUB_CONTENT + ", " + POPUP);
        positioner._templOuterPortal = outer?.matches('[data-slot="' + slot + '-portal"]') ? outer : null;
      }
      return positioner._templOuterPortal;
    }

    function mountPortal(positioner) {
      // Base UI mounts the popup fresh on every open: no tab order the portal
      // saved from the previous one (FloatingPortal's disableFocusInside).
      positioner.querySelectorAll("[data-tabindex]").forEach((el) => el.removeAttribute("data-tabindex"));
      const outer = outerPortalOf(positioner);
      if (outer) {
        window.templ.portal.render(outer);
        outer.hidden = false;
      }
      const node = portalNodeOf(positioner);
      window.templ.portal.render(node);
      node.hidden = false;
      positioner.hidden = false;
    }

    function unmountPortal(positioner) {
      positioner.hidden = true;
      portalNodeOf(positioner).hidden = true;
      const outer = outerPortalOf(positioner);
      if (outer) outer.hidden = true;
    }

    // ----- state attributes -----------------------------------------------------

    // MenuRoot's instantType, rendered as data-instant on the positioner and
    // the popup: "click" for a press without a pointer (a keyboard click or a
    // context menu key), "dismiss" for Escape, none otherwise.
    function setInstant(positioner, popup, details) {
      const { reason, event, open } = details || {};
      let instant = null;
      if ((reason === "trigger-press" || reason === "item-press") && event?.detail === 0 && event.isTrusted) {
        instant = "click";
      } else if (!open && (reason === "escape-key" || reason == null)) {
        instant = "dismiss";
      }
      [positioner, popup].forEach((el) => {
        if (instant) el.setAttribute("data-instant", instant);
        else el.removeAttribute("data-instant");
      });
    }

    // MenuPositioner's InternalBackdrop while a modal menu is mounted, with a
    // hole over the cutout (the dropdown trigger).
    function mountBackdrop(positioner, cutout) {
      window.templ.internalBackdrop.mount(positioner, cutout);
    }

    // CheckboxItemIndicator and RadioItemIndicator, inside shadcn's
    // indicator span. It renders the item's state and mounts while checked.
    function indicatorOf(item) {
      return item.querySelector(":scope > span > span[aria-hidden]");
    }

    function setChecked(item, checked) {
      item.toggleAttribute("data-checked", checked);
      item.toggleAttribute("data-unchecked", !checked);
      item.setAttribute("aria-checked", checked ? "true" : "false");
      const indicator = indicatorOf(item);
      if (indicator) indicator.hidden = !checked;
    }

    // Base UI renders an id on every item, label and submenu trigger, and
    // a group names its label.
    function wireIds(popup) {
      let n = 0;
      popup.querySelectorAll(ITEM_SELECTOR + ", " + LABEL).forEach((el) => {
        if (!el.id) el.id = popup.id + "-" + ++n;
      });
      popup.querySelectorAll(GROUP).forEach((group) => {
        const label = [...group.querySelectorAll(LABEL)].find((l) => l.closest(GROUP) === group);
        if (label && !group.hasAttribute("aria-labelledby")) group.setAttribute("aria-labelledby", label.id);
      });
    }

    // ----- list navigation and typeahead ---------------------------------------

    // The menu an item belongs to: the root popup or a submenu's popup.
    function containerOf(el) {
      return el.closest(SUB_CONTENT + ", " + POPUP);
    }

    // The menu's list, disabled items included (Base UI's menus pass an empty
    // disabledIndices, so disabled items are highlighted too).
    function itemsIn(popup) {
      return [...popup.querySelectorAll(ITEM_SELECTOR)].filter((item) => containerOf(item) === popup);
    }

    // The item's highlight, with the roving tabindex of useMenuItemCommonProps.
    // A submenu trigger also stays in the tab order while its submenu is open.
    function highlight(popup, index) {
      popup._templActiveIndex = index;
      itemsIn(popup).forEach((item, i) => {
        item.toggleAttribute("data-highlighted", i === index);
        indicatorOf(item)?.toggleAttribute("data-highlighted", i === index);
        item.tabIndex = i === index || isOpenSubTrigger(item) ? 0 : -1;
      });
    }

    function isOpenSubTrigger(item) {
      return item.matches(SUB_TRIGGER) && item.getAttribute("aria-expanded") === "true";
    }

    // MenuSubmenuTrigger's onBlur: focus that leaves it, into its submenu too,
    // clears the parent menu's highlight.
    function onSubTriggerBlur(event) {
      const trigger = event.currentTarget;
      if (trigger.hasAttribute("data-highlighted")) highlight(containerOf(trigger), null);
    }

    function setSubTriggerOpen(trigger, open) {
      trigger.setAttribute("aria-expanded", open ? "true" : "false");
      trigger.toggleAttribute("data-popup-open", open);
      // SubmenuTrigger renders aria-controls while its submenu is open.
      if (open) trigger.setAttribute("aria-controls", trigger.getAttribute("data-templ-controls"));
      else trigger.removeAttribute("aria-controls");
      trigger.tabIndex = open || trigger.hasAttribute("data-highlighted") ? 0 : -1;
    }

    // MenuRoot's useListNavigation and useTypeahead for one menu popup.
    // options are the root's: { nested, parentOrientation, openOnArrowKeyDown }.
    function startListNavigation(popup, reference, isPopupOpen, onOpenChange, options) {
      const items = () => itemsIn(popup);
      const activeIndex = () => popup._templActiveIndex ?? null;
      popup._templNav = window.templ.listNavigation.useListNavigation({
        floating: popup,
        reference,
        items,
        activeIndex,
        onNavigate: (index) => highlight(popup, index),
        onOpenChange,
        isOpen: isPopupOpen,
        loopFocus: true,
        disabledIndices: [],
        // MenuRoot's rtl: useDirection.
        rtl: window.templ.direction.useDirection(popup) === "rtl",
        ...options,
      });
      popup._templTypeahead = window.templ.typeahead.useTypeahead({
        elements: [reference, popup],
        labels: () => items().map((item) => item.textContent.trim()),
        items,
        activeIndex,
        isOpen: isPopupOpen,
        onMatch(index) {
          if (!isPopupOpen() || index === activeIndex()) return;
          highlight(popup, index);
          popup._templNav.sync();
        },
        resetMs: 500,
      });
    }

    function stopListNavigation(popup) {
      popup._templNav?.cleanup();
      popup._templTypeahead?.cleanup();
      popup._templNav = null;
      popup._templTypeahead = null;
    }

    // ----- root ------------------------------------------------------------------

    // What every root open does once positioned: the enter transition, the
    // list navigation and the submenus' declared state.
    function afterRootOpen(positioner, details) {
      const popup = popupFor(positioner);
      setInstant(positioner, popup, { ...details, open: true });
      window.templ.transition.open({ positioner, parts: [popup] });
      popup._templNav?.open();
      popup._templTypeahead?.reset();
      syncSubState(positioner);
    }

    // What every root close does: submenus close at once, the backdrop goes
    // inert, the exit transition runs, then the menu unmounts (its focus
    // manager returns focus then, through onUnmount).
    function closeRoot(positioner, details, onUnmount) {
      const popup = popupFor(positioner);
      popup._templAllowMouseEnter = false;
      popup._templNav?.close();
      popup._templTypeahead?.reset();
      setInstant(positioner, popup, { ...details, open: false });
      positioner._templSubs?.forEach(closeSubNow);
      window.templ.internalBackdrop.inert(positioner);
      window.templ.transition.close({ positioner, parts: [popup] }, popup, () => {
        onUnmount();
        window.templ.internalBackdrop.remove(positioner);
        unmountPortal(positioner);
      });
    }

    // Every submenu of the tree, collected before they portal.
    function initRoot(positioner) {
      const popup = popupFor(positioner);
      wireIds(popup);
      positioner._templSubs = [...popup.querySelectorAll(SUB_TRIGGER)];
      positioner._templSubs.forEach(initSub);
    }

    function destroyRoot(positioner) {
      stopListNavigation(popupFor(positioner));
      positioner._templSubs?.forEach(destroySub);
      window.templ.internalBackdrop.remove(positioner);
      window.templ.portal.remove(portalNodeOf(positioner));
    }

    // ----- submenus -------------------------------------------------------------

    // A submenu is its trigger. The popup portals on open, so the link is kept
    // from the declaration.
    function subContentOf(sub) {
      if (!sub._templSubContent) {
        const popup = document.getElementById(sub.getAttribute("data-templ-controls") || "");
        if (popup?.matches(SUB_CONTENT)) {
          sub._templSubContent = popup;
          popup._templSub = sub;
        }
      }
      return sub._templSubContent || null;
    }

    // The store's open state, set when the change is requested.
    function isSubOpen(sub) {
      return !!sub._templIsOpen;
    }

    // The parent menu's popup: the root popup or a submenu's popup.
    function parentPopupOf(sub) {
      return containerOf(sub);
    }

    // The subs whose parent menu is this popup.
    function childSubsOf(popup, all) {
      return all.filter((sub) => parentPopupOf(sub) === popup);
    }

    function subsOfTree(sub) {
      return positionerOf(sub)?._templSubs || [];
    }

    // A submenu's MenuRoot: list navigation nested in its parent menu, the
    // trigger's useClick and useHoverReferenceInteraction, the popup's
    // useHoverFloatingInteraction.
    function initSub(sub) {
      const content = subContentOf(sub);
      if (!content) return;
      const positioner = content.parentElement;
      sub.addEventListener("blur", onSubTriggerBlur);
      startListNavigation(content, sub, () => isSubOpen(sub),
        (open) => requestSubOpenChange(sub, open), { nested: true, parentOrientation: "vertical" });
      const hover = window.templ.hover;
      const disabled = () => sub.getAttribute("aria-disabled") === "true";
      // hoverEnabled of the submenu's store: off after the pointer moved in its
      // popup or one of its own submenus opened, on again once it closes.
      const hoverEnabled = () => sub._templHoverEnabled !== false;
      sub._templHover = hover.createHoverInteraction({
        isOpen: () => isSubOpen(sub),
        onOpenChange: (open, reason, event) => requestSubOpenChange(sub, open, { reason, event }),
        openEventType: () => sub._templOpenEventType ?? null,
        domReference: () => sub,
        floating: () => (positioner.hidden ? null : positioner),
        placement: () => positioner.getAttribute("data-side") || "right",
        triggers: () => [sub],
        // The scope Base UI resolves for a submenu: the parent menu's popup,
        // through its data-rootownerid.
        parentFloating: () => parentPopupOf(sub),
      });
      sub._templCleanups = [
        window.templ.click.useClick(sub, {
          event: "mousedown",
          toggle: false,
          ignoreMouse: true,
          stickIfOpen: false,
          isOpen: () => isSubOpen(sub),
          openEventType: () => sub._templOpenEventType ?? null,
          onOpenChange(nextOpen, event) {
            if (!disabled()) requestSubOpenChange(sub, nextOpen, { reason: "trigger-press", event });
          },
        }),
        hover.useHoverReferenceInteraction(sub, sub._templHover, {
          enabled: () => hoverEnabled() && !disabled(),
          handleClose: hover.safePolygon({ blockPointerEvents: true }),
          mouseOnly: true,
          move: true,
          restMs: SUBMENU_DELAY,
          delay: { open: SUBMENU_DELAY, close: SUBMENU_CLOSE_DELAY },
          // The menu opened under a resting pointer: no submenu opens until it moves.
          shouldOpen: () => parentPopupOf(sub)?._templAllowMouseEnter === true,
          isClosing: () => window.templ.transition.isEnding(content),
        }),
        hover.useHoverFloatingInteraction(sub._templHover, { enabled: hoverEnabled, closeDelay: SUBMENU_CLOSE_DELAY }),
      ];
    }

    function destroySub(sub) {
      const content = subContentOf(sub);
      if (!content) return;
      sub.removeEventListener("blur", onSubTriggerBlur);
      closeSubNow(sub);
      sub._templCleanups?.forEach((cleanup) => cleanup());
      sub._templCleanups = null;
      sub._templHover?.dispose();
      sub._templHover = null;
      stopListNavigation(content);
    }

    // The submenu's MenuPopup focus manager: non modal, no initial focus, focus
    // returns to the submenu trigger.
    function startSubFocusManager(sub) {
      const content = subContentOf(sub);
      if (content._templFocus) {
        content._templFocus.open();
        return;
      }
      content._templFocus = window.templ.focusManager.useFloatingFocusManager({
        floating: content.parentElement,
        reference: sub,
        modal: false,
        initialFocus: false,
        restoreFocus: true,
        previousFocusableElement: sub,
        onOpenChange: (open, reason, event) => requestSubOpenChange(sub, open, { reason, event }),
      });
    }

    function stopSubFocusManager(content) {
      content._templFocus?.unmount();
      content._templFocus = null;
    }

    function stopSubPositioning(content) {
      content._templPositionCleanup?.();
      content._templPositionCleanup = null;
    }

    function openSub(sub, details = {}) {
      const content = subContentOf(sub);
      if (!content) return;
      const positioner = content.parentElement;
      sub._templIsOpen = true;
      sub._templOpenEventType = details.event?.type ?? null;
      mountPortal(positioner);
      // The submenu's MenuRoot useDismiss: Escape closes only the submenu
      // (closeParentOnEsc is false).
      content._templDismiss ??= window.templ.dismiss.useDismiss({
        floating: positioner,
        reference: sub,
        onOpenChange: (open, reason, event) => requestSubOpenChange(sub, open, { reason, event }),
      });
      startSubFocusManager(sub);
      sub._templHover?.openChange(true, details.reason);
      stopSubPositioning(content);
      const positioning = window.templ.anchorPositioning.useAnchorPositioning({
        anchor: sub,
        positioner,
        parts: [positioner, content],
        ...subPositioning,
      });
      content._templPositionCleanup = positioning.cleanup;
      positioning.positioned.then(() => {
        if (positioner.hidden) return; // closed meanwhile
        setInstant(positioner, content, { ...details, open: true });
        window.templ.transition.open({ positioner, parts: [content] });
        setSubTriggerOpen(sub, true);
        content._templNav?.open();
        content._templTypeahead?.reset();
      });
    }

    // The store's reset when a submenu closes: hover on again, no pointer move
    // seen in its popup yet.
    function resetSubState(sub, content) {
      sub._templIsOpen = false;
      sub._templOpenEventType = null;
      sub._templHoverEnabled = true;
      content._templAllowMouseEnter = false;
    }

    // Closes with the exit animation. details { reason, event } of the close,
    // for the focus manager and the hover interaction.
    function closeSub(sub, details = {}) {
      const content = subContentOf(sub);
      if (!content) return;
      const positioner = content.parentElement;
      resetSubState(sub, content);
      sub._templHover?.openChange(false, details.reason);
      content._templDismiss?.();
      content._templDismiss = null;
      content._templFocus?.close(details);
      content._templNav?.close();
      content._templTypeahead?.reset();
      setInstant(positioner, content, { ...details, open: false });
      window.templ.transition.close({ positioner, parts: [content] }, content, () => {
        stopSubPositioning(content);
        stopSubFocusManager(content);
        unmountPortal(positioner);
      });
      setSubTriggerOpen(sub, false);
    }

    // Closes immediately (used when the whole menu goes away).
    function closeSubNow(sub) {
      const content = subContentOf(sub);
      if (!content) return;
      const positioner = content.parentElement;
      const wasOpen = isSubOpen(sub);
      resetSubState(sub, content);
      if (wasOpen) sub._templHover?.openChange(false);
      stopSubPositioning(content);
      content._templDismiss?.();
      content._templDismiss = null;
      content._templNav?.close();
      stopSubFocusManager(content);
      unmountPortal(positioner);
      window.templ.transition.reset({ positioner, parts: [content] }, false);
      setSubTriggerOpen(sub, false);
    }

    function requestSubOpenChange(sub, nextOpen, details = {}) {
      const content = subContentOf(sub);
      if (!content || isSubOpen(sub) === nextOpen) return false;
      const accepted = sub.dispatchEvent(
        new CustomEvent(eventPrefix + "-sub-open-change", {
          bubbles: true,
          cancelable: true,
          detail: { open: nextOpen },
        }),
      );
      // Controlled: the Base UI open prop on the SubmenuRoot, the owner commits.
      if (!accepted || content.parentElement.hasAttribute("data-templ-open")) return false;
      sub._templSubOpen = nextOpen;
      const all = subsOfTree(sub);
      const parentPopup = parentPopupOf(sub);
      if (nextOpen) {
        // MenuPositioner's menuopenchange: the parent menu stops closing on
        // hover, a sibling submenu closes.
        if (parentPopup?._templSub) parentPopup._templSub._templHoverEnabled = false;
        childSubsOf(parentPopup, all).forEach((other) => {
          if (other !== sub) requestSubOpenChange(other, false, { reason: "sibling-open" });
        });
        openSub(sub, details);
      } else {
        // A submenu closes with its parent.
        childSubsOf(content, all).forEach((child) => requestSubOpenChange(child, false, details));
        closeSub(sub, details);
      }
      return true;
    }

    function syncSubState(positioner) {
      positioner._templSubs?.forEach((sub) => {
        const content = subContentOf(sub);
        if (!content) return;
        const subPositioner = content.parentElement;
        // Last requested state, else the server's open or defaultOpen.
        const shouldOpen = sub._templSubOpen ?? (
          subPositioner.getAttribute("data-templ-open") === "true" ||
          subPositioner.hasAttribute("data-templ-default-open"));
        if (shouldOpen && !isSubOpen(sub)) openSub(sub);
        else if (!shouldOpen && isSubOpen(sub)) closeSubNow(sub);
      });
    }

    // ----- events -----------------------------------------------------------------

    // The popup's onMouseMove and onClick in MenuRoot, and the items'
    // itemhover: a pointer move allows hover opening in that menu, turns off
    // the hover close of a submenu, and a move over another item of the parent
    // menu closes the open submenu there.
    function menuPopupOf(target) {
      const popup = target.closest?.(SUB_CONTENT + ", " + POPUP);
      if (!popup) return null;
      return popup.matches(SUB_CONTENT) || isPositioner(popup.parentElement) ? popup : null;
    }

    document.addEventListener("mousemove", (e) => {
      if (!(e.target instanceof Element)) return;
      const popup = menuPopupOf(e.target);
      if (!popup) return;
      popup._templAllowMouseEnter = true;
      if (popup._templSub) popup._templSub._templHoverEnabled = false;
      const item = e.target.closest(ITEM_SELECTOR);
      if (!item || containerOf(item) !== popup) return;
      childSubsOf(popup, positionerOf(popup)?._templSubs || []).forEach((sub) => {
        if (isSubOpen(sub) && sub !== item) {
          requestSubOpenChange(sub, false, { reason: "sibling-open", event: e });
        }
      });
    });

    function isDisabled(item) {
      return item.getAttribute("aria-disabled") === "true";
    }

    // useButton on a non native item: Enter clicks on keydown, Space too
    // (a composite item), unless typeahead took the Space. A link item clicks
    // natively on Enter.
    document.addEventListener("keydown", (e) => {
      const item = e.target;
      if (!(item instanceof Element) || !item.matches(ITEM_SELECTOR) || !menuPopupOf(item)) return;
      if (item.matches(SUB_TRIGGER) || isDisabled(item)) return;
      if (e.key === " ") {
        if (e.defaultPrevented) return;
        e.preventDefault();
        item.click();
      } else if (e.key === "Enter" && item.tagName !== "A") {
        e.preventDefault();
        item.click();
      }
    });

    document.addEventListener("click", (e) => {
      if (!(e.target instanceof Element)) return;
      const popup = menuPopupOf(e.target);
      if (!popup) return;
      if (popup._templSub) popup._templSub._templHoverEnabled = false;

      // Checkbox items toggle and keep the menu open.
      const checkbox = e.target.closest('[data-slot="' + slot + '-checkbox-item"]');
      if (checkbox) {
        if (isDisabled(checkbox)) return;
        const on = checkbox.hasAttribute("data-checked");
        const accepted = checkbox.dispatchEvent(new CustomEvent(eventPrefix + "-checked-change", {
          bubbles: true,
          cancelable: true,
          detail: { checked: !on },
        }));
        if (accepted && !checkbox.hasAttribute("data-templ-checked")) setChecked(checkbox, !on);
        return;
      }

      // Radio items select within their group and keep the menu open.
      const radio = e.target.closest('[data-slot="' + slot + '-radio-item"]');
      if (radio) {
        if (isDisabled(radio)) return;
        const group = radio.closest('[data-slot="' + slot + '-radio-group"]');
        const accepted = (group || radio).dispatchEvent(new CustomEvent(eventPrefix + "-value-change", {
          bubbles: true,
          cancelable: true,
          detail: { value: radio.getAttribute("data-templ-value") },
        }));
        if (accepted && group && !group.hasAttribute("data-templ-value")) {
          group.querySelectorAll('[data-slot="' + slot + '-radio-item"]').forEach((r) => setChecked(r, false));
          setChecked(radio, true);
        }
        return;
      }

      // An item press closes the whole menu (closeOnClick).
      const item = e.target.closest('[data-slot="' + slot + '-item"]');
      if (item && !isDisabled(item) && item.getAttribute("data-templ-close-on-click") !== "false") {
        const positioner = positionerOf(item);
        if (positioner) onItemPress(positioner, e);
      }
    });

    return {
      POPUP,
      SUB_TRIGGER,
      isPositioner,
      popupFor,
      positionerById,
      allPositioners,
      positionerOf,
      isOpen,
      mountPortal,
      mountBackdrop,
      startListNavigation,
      stopListNavigation,
      afterRootOpen,
      closeRoot,
      closeSubs: (positioner) => positioner._templSubs?.forEach(closeSubNow),
      initRoot,
      destroyRoot,
    };
  }

  window.templ = window.templ || {};
  window.templ.menu = { create };
})();

// components/baseui/menu_root.js
// Port of @base-ui/react menu (1.6.0) MenuRoot with MenuTrigger for a root
// menu, on top of menu.js: what shadcn's dropdown menu and the menus of
// shadcn's menubar share. A root menu opens from its trigger, positions
// against it, traps focus non modally, and is modal (internal backdrop and
// scroll lock) unless its parent says otherwise. The menubar passes its parent
// part: the cutout of the backdrop, the click and focus behavior of a
// MenuTrigger in a menubar, the open change it tracks for hasSubmenuOpen.
//
//   const root = window.templ.menuRoot.create({
//     slot, event, trigger,     the slot prefix, the event prefix, the trigger selector
//     subPositioning,           shadcn's <slot>-sub-content positioner props
//     parent,                   optional menubar part, see below
//   })
//
// parent: {
//   cutout(trigger)                          the backdrop's hole
//   focusGuards                              false: no MenuTrigger focus guards
//   clickEvent(trigger, isOpenedByTrigger)   useClick's event
//   listNavigation                           extra useListNavigation options
//   instant(reason)                          MenuRoot's instantType for the reason, or undefined
//   onOpenChange(content, open, details)     after a change was applied
//   keydown(event)                           the popup's keyboardEventRelay
//   rootId(trigger)                          rendered as the popup's data-rootownerid
// }
(function () {
  "use strict";

  function create(config) {
    const { slot, event: eventPrefix, trigger: TRIGGER, subPositioning, parent = null } = config;

    const menu = window.templ.menu.create({
      slot,
      event: eventPrefix,
      subPositioning,
      onItemPress: (content, event) => requestOpenChange(content, false, { reason: "item-press", event }),
    });

    // The id is the popup's, like Base UI's.
    function triggerFor(content) {
      return document.querySelector('[data-templ-controls="' + menu.popupFor(content).id + '"]');
    }

    function contentFor(trigger) {
      return menu.positionerById(trigger.getAttribute("data-templ-controls"));
    }

    // The root trigger an event target sits in, if any.
    function triggerOf(target) {
      const trigger = target.closest && target.closest(TRIGGER);
      return trigger && !trigger.matches(menu.SUB_TRIGGER) && contentFor(trigger) ? trigger : null;
    }

    // MenuPositioner: useAnchorPositioning with the menu collision avoidance,
    // while the menu is mounted. It keeps the menu attached to its trigger
    // while ancestors move, resize, scroll or shift layout.
    function startAutoPositioning(content, trigger) {
      stopAutoPositioning(content);
      const positioning = window.templ.anchorPositioning.useAnchorPositioning({
        anchor: trigger,
        positioner: content,
        parts: [content, menu.popupFor(content)],
        // Read at open, like Base UI reads its side prop on render; a block that
        // switches side per viewport updates data-templ-side itself.
        side: content.getAttribute("data-templ-side") || "bottom",
        align: content.getAttribute("data-templ-align") || "start",
        sideOffset: parseFloat(content.getAttribute("data-templ-side-offset")) || 0,
        alignOffset: parseFloat(content.getAttribute("data-templ-align-offset")) || 0,
        collisionAvoidance: { fallbackAxisSide: "none" },
      });
      content._templPositionCleanup = positioning.cleanup;
      return positioning.positioned;
    }

    function stopAutoPositioning(content) {
      content._templPositionCleanup?.();
      content._templPositionCleanup = null;
    }

    // MenuPopup's FloatingFocusManager: non modal, with MenuTrigger's focus
    // guards around the trigger while it is open (not in a menubar, where the
    // trigger is a composite item).
    function startFocusManager(content, trigger) {
      if (content._templFocus) {
        content._templFocus.open(content._templOpenMethod === "programmatic" ? null : content._templOpenMethod);
        return;
      }
      const onOpenChange = (open, reason, event) => requestOpenChange(content, open, { reason, event });
      if (!parent || parent.focusGuards !== false) {
        content._templTriggerGuards = window.templ.triggerFocusGuards.attach(trigger, {
          positioner: content,
          beforeContentFocusGuard: () => content._templFocus?.beforeGuard,
          onClose: (event) => onOpenChange(false, "focus-out", event),
        });
      }
      content._templFocus = window.templ.focusManager.useFloatingFocusManager({
        floating: content,
        reference: trigger,
        modal: false,
        openInteractionType: content._templOpenMethod === "programmatic" ? null : content._templOpenMethod,
        restoreFocus: true,
        previousFocusableElement: trigger,
        nextFocusableElement: content._templTriggerGuards?.focusTarget,
        onOpenChange,
      });
    }

    function stopFocusManager(content) {
      content._templFocus?.unmount();
      content._templFocus = null;
      content._templTriggerGuards?.remove();
      content._templTriggerGuards = null;
    }

    // MenuRoot's instantType in a menubar ("group" for a switch between its
    // menus), rendered after menu.js' own on the positioner and the popup.
    function applyParentInstant(content, details) {
      const instant = parent?.instant?.(details.reason);
      if (!instant) return;
      content.setAttribute("data-instant", instant);
      menu.popupFor(content).setAttribute("data-instant", instant);
    }

    // details { reason, event } of the open.
    function open(content, trigger, details = {}) {
      menu.allPositioners().forEach((c) => {
        if (c !== content) close(c, parent ? { reason: "sibling-open" } : {});
      });
      content._templOpenMethod = trigger._templOpenMethod || "programmatic";
      content._templOpenReason = details.reason;
      menu.mountPortal(content);
      // A modal menu (Base UI's default) renders the internal backdrop with a
      // hole over the trigger, or over the whole menubar.
      menu.mountBackdrop(content, parent?.cutout?.(trigger) ?? trigger);
      // MenuTrigger renders aria-controls while the popup is open
      // (triggerPopupId).
      trigger.setAttribute("aria-controls", menu.popupFor(content).id);
      const rootId = parent?.rootId?.(trigger);
      if (rootId) menu.popupFor(content).setAttribute("data-rootownerid", rootId);
      content._templDismiss ??= window.templ.dismiss.useDismiss({
        floating: content,
        reference: trigger,
        onOpenChange: (open, reason, event) => requestOpenChange(content, open, { reason, event }),
      });
      startFocusManager(content, trigger);
      parent?.onOpenChange?.(content, true, details);

      // Positioned first, then the enter animation plays in place.
      const finish = () => {
        if (content.hidden || !content.isConnected) return;
        // useAnchoredPopupScrollLock for the modal menu, measured on the
        // positioned popup for touch opens; a menu opened by hover locks none.
        content._templReleaseScroll?.();
        content._templReleaseScroll = window.templ.scrollLock.anchoredPopup(
          details.reason !== "trigger-hover", content._templOpenMethod === "touch", content, trigger,
        );
        trigger.setAttribute("aria-expanded", "true");
        trigger.setAttribute("data-popup-open", "");
        trigger.setAttribute("data-pressed", "");
        menu.afterRootOpen(content, details);
        applyParentInstant(content, details);
      };
      startAutoPositioning(content, trigger).then(finish, finish);
    }

    // details { reason, event } of the close, for the focus manager.
    function close(content, details = {}) {
      if (content.hidden) return;
      content._templDismiss?.();
      content._templDismiss = null;
      content._templFocus?.close(details);
      const trigger = triggerFor(content);
      if (trigger) {
        trigger.removeAttribute("aria-controls");
        trigger.setAttribute("aria-expanded", "false");
        trigger.removeAttribute("data-popup-open");
        trigger.removeAttribute("data-pressed");
      }
      // Positioned until it unmounts, like Base UI. Unmounting the focus
      // manager returns focus.
      menu.closeRoot(content, details, () => {
        stopAutoPositioning(content);
        stopFocusManager(content);
      });
      applyParentInstant(content, details);
      content._templReleaseScroll?.();
      content._templReleaseScroll = null;
      parent?.onOpenChange?.(content, false, details);
    }

    function requestOpenChange(content, nextOpen, details = {}) {
      if (!content || menu.isOpen(content) === nextOpen) return false;
      const accepted = content.dispatchEvent(
        new CustomEvent(eventPrefix + "-open-change", {
          bubbles: true,
          cancelable: true,
          detail: { open: nextOpen },
        }),
      );
      if (!accepted || content.hasAttribute("data-templ-open")) return false;
      const trigger = triggerFor(content);
      if (nextOpen && trigger) open(content, trigger, details);
      else if (!nextOpen) close(content, details);
      return true;
    }

    // MenuTrigger's onMouseMove: hover opening of submenus starts once the
    // pointer moved.
    document.addEventListener("mousemove", (e) => {
      if (!(e.target instanceof Element)) return;
      const trigger = triggerOf(e.target);
      const popup = trigger && menu.popupFor(contentFor(trigger));
      if (popup) popup._templAllowMouseEnter = true;
    });

    // MenuTrigger's useClick: a mouse or touch press opens on mousedown, one
    // frame later, Enter and Space arrive as the click a native button fires.
    // In a menubar the trigger of the open menu closes it on click.
    function listenForClick(content, trigger) {
      const options = {
        isOpen: () => menu.isOpen(content),
        onOpenChange(nextOpen, event, pointerType) {
          if (trigger.disabled || trigger.getAttribute("aria-disabled") === "true") return;
          trigger._templOpenMethod = pointerType || "keyboard";
          requestOpenChange(content, nextOpen, { reason: "trigger-press", event });
        },
      };
      if (!parent) return window.templ.click.useClick(trigger, { ...options, event: "mousedown" });
      // useClick's event follows isOpenedByThisTrigger: re-created on change.
      let cleanup = null;
      let event = null;
      const sync = () => {
        const next = parent.clickEvent(trigger, menu.isOpen(content));
        if (next === event) return;
        cleanup?.();
        event = next;
        cleanup = window.templ.click.useClick(trigger, { ...options, event, stickIfOpen: false });
      };
      sync();
      content._templSyncClick = sync;
      return () => cleanup?.();
    }

    // A content unmounts with its portal owner: a portaled one is removed from
    // <body> then.
    window.templ.lifecycle.register(menu.POPUP, {
      init(popup) {
        const content = popup.parentElement;
        const trigger = menu.isPositioner(content) && triggerFor(content);
        if (!trigger) return;
        content._templClickCleanup = listenForClick(content, trigger);
        menu.startListNavigation(popup, trigger, () => menu.isOpen(content), (open, reason, event) => {
          if (trigger.disabled || trigger.getAttribute("aria-disabled") === "true") return;
          if (open) trigger._templOpenMethod = "keyboard";
          requestOpenChange(content, open, { reason, event });
        }, parent?.listNavigation);
        if (parent?.keydown) popup.addEventListener("keydown", parent.keydown);
        menu.initRoot(content);
        // Server-side open state (Base UI open or defaultOpen).
        if (content.getAttribute("data-templ-open") === "true" || content.hasAttribute("data-templ-default-open")) {
          open(content, trigger);
        }
      },
      destroy(popup) {
        const content = popup.parentElement;
        if (!menu.isPositioner(content)) return;
        content._templClickCleanup?.();
        if (parent?.keydown) popup.removeEventListener("keydown", parent.keydown);
        stopAutoPositioning(content);
        content._templReleaseScroll?.();
        content._templReleaseScroll = null;
        content._templDismiss?.();
        stopFocusManager(content);
        menu.destroyRoot(content);
      },
    });

    return { menu, triggerFor, contentFor, requestOpenChange, open, close, isOpen: menu.isOpen };
  }

  window.templ = window.templ || {};
  window.templ.menuRoot = { create };
})();

// components/baseui/portal.js
// Port of @base-ui/react floating-ui-react/components/FloatingPortal.tsx (1.6.0).
//
// render(node) moves a [data-base-ui-portal] node to its container while its
// component stays where it was declared. That declaration site is the portal
// owner, and lifecycle.js unmounts the portaled subtree once the owner leaves
// the document. The container is the portal node of an enclosing portal, or
// <body> (useFloatingPortalNode). remove(node) unmounts it with its guards.
//
// setFocusManagerState(node, state) is the source's portal context: a non
// modal focus manager inside the node gets the outside guards at the
// declaration site, which move Tab between the trigger and the portaled
// content, and the node's tabbables are taken out of the tab order while
// focus is outside of it.
(function () {
  "use strict";

  let uid = 0;
  const t = () => window.templ.tabbable;

  // The React tree pendant: the parent of a portal node is where it was
  // declared, of every other node its DOM parent.
  function treeParent(node) {
    return node._templPortalOwner || node.parentNode;
  }

  function containerFor(node) {
    return node._templPortalOwner?.closest("[data-base-ui-portal]") || document.body;
  }

  // Appends on every open, so paint order follows open order like the
  // portal nodes React creates on mount. A portal declared right inside its
  // container's node (a Portal part around a submenu) comes before its own
  // outside guards there, as upstream renders it. target is createPortal's
  // container when it is not the portal node's own (NavigationMenu's
  // viewport).
  function render(node, target) {
    if (!node._templPortalOwner) node._templPortalOwner = node.parentElement;
    if (target) {
      target.appendChild(node);
      return;
    }
    if (!node.id) node.id = "templ-portal-" + ++uid;
    const container = containerFor(node);
    const owner = node._templPortalOwner;
    if (container !== document.body && owner.parentElement === container) container.insertBefore(node, node._templPortalGuards?.beforeOutside || owner);
    else container.appendChild(node);
  }

  function remove(node) {
    if (!node) return;
    setFocusManagerState(node, null);
    if (node.isConnected) node.remove();
  }

  // Tabbables inside the node count only once focus entered it, through an
  // outside guard or a pointer.
  function onFocus(event) {
    const node = event.currentTarget;
    if (!event.relatedTarget || !t().isOutsideEvent(event)) return;
    if (event.type === "focusin") {
      if (node._templFocusInsideDisabled) {
        t().enableFocusInside(node);
        node._templFocusInsideDisabled = false;
      }
    } else {
      t().disableFocusInside(node);
      node._templFocusInsideDisabled = true;
    }
  }

  function createOutsideGuards(node) {
    const { createFocusGuard } = window.templ.focusManager;
    const state = () => node._templFocusState;
    const beforeOutside = createFocusGuard("outside", (event) => {
      if (t().isOutsideEvent(event, node)) {
        state()?.beforeInside?.focus();
      } else {
        const domReference = state()?.domReference;
        if (domReference) t().getPreviousTabbable(domReference)?.focus();
      }
    });
    const afterOutside = createFocusGuard("outside", (event) => {
      if (t().isOutsideEvent(event, node)) {
        state()?.afterInside?.focus();
      } else {
        const domReference = state()?.domReference;
        if (domReference) t().getNextTabbable(domReference)?.focus();
        if (state()?.closeOnFocusOut) state()?.onOpenChange?.(false, "focus-out", event);
      }
    });
    const owns = document.createElement("span");
    owns.setAttribute("aria-owns", node.id);
    owns.style.cssText = "clip-path:inset(50%);position:fixed;top:0;left:0";
    node._templPortalOwner?.before(beforeOutside, owns, afterOutside);
    return { beforeOutside, afterOutside, owns };
  }

  function setFocusManagerState(node, state) {
    if (!node) return;
    node._templFocusState = state;
    const nonModal = !!state && !state.modal;

    if (nonModal && !node._templFocusListening) {
      node.addEventListener("focusin", onFocus, true);
      node.addEventListener("focusout", onFocus, true);
      node._templFocusListening = true;
    } else if (!nonModal && node._templFocusListening) {
      node.removeEventListener("focusin", onFocus, true);
      node.removeEventListener("focusout", onFocus, true);
      node._templFocusListening = false;
    }
    // Tabbable again before the focus manager's queued initial focus runs.
    if (state?.open && node._templFocusInsideDisabled) {
      t().enableFocusInside(node);
      node._templFocusInsideDisabled = false;
    }

    const shouldRenderGuards = nonModal && state.open;
    if (shouldRenderGuards && !node._templPortalGuards) {
      node._templPortalGuards = createOutsideGuards(node);
    } else if (!shouldRenderGuards && node._templPortalGuards) {
      const { beforeOutside, afterOutside, owns } = node._templPortalGuards;
      beforeOutside.remove();
      owns.remove();
      afterOutside.remove();
      node._templPortalGuards = null;
    }
  }

  window.templ = window.templ || {};
  window.templ.portal = { render, remove, setFocusManagerState, treeParent };
})();

// components/baseui/scroll_lock.js
// Port of @base-ui/utils/useScrollLock.ts. Helpers from useTimeout.ts,
// useAnimationFrame.ts, platform/os.ts, platform/engine.ts, @floating-ui/utils/dom,
// and @base-ui/react/utils/useAnchoredPopupScrollLock.ts live here as well.
(function () {
  "use strict";

  // @base-ui/utils/platform/{os,engine}.ts
  const lowerPlatform = navigator.platform.toLowerCase();
  const ios = /^i(os$|p)/.test(lowerPlatform) ||
    (lowerPlatform === "macintel" && navigator.maxTouchPoints > 1);
  const webkit = typeof CSS !== "undefined" && !!CSS.supports?.("-webkit-backdrop-filter:none");

  const ownerDocument = (referenceElement) => referenceElement?.ownerDocument || document;
  const ownerWindow = (referenceElement) =>
    (referenceElement?.nodeType === 9 ? referenceElement : ownerDocument(referenceElement)).defaultView || window;

  // @floating-ui/utils/dom: isOverflowElement
  function isOverflowElement(element) {
    const { overflow, overflowX, overflowY, display } = ownerWindow(element).getComputedStyle(element);
    return /auto|scroll|overlay|hidden|clip/.test(overflow + overflowY + overflowX) &&
      display !== "inline" && display !== "contents";
  }

  // @base-ui/utils/useTimeout.ts (the imperative helper; no React lifecycle).
  class Timeout {
    static create() { return new Timeout(); }
    currentId = 0;
    start(delay, fn) {
      this.clear();
      this.currentId = setTimeout(() => {
        this.currentId = 0;
        fn();
      }, delay);
    }
    isStarted() { return this.currentId !== 0; }
    clear = () => {
      if (this.currentId !== 0) {
        clearTimeout(this.currentId);
        this.currentId = 0;
      }
    };
  }

  // @base-ui/utils/useAnimationFrame.ts, including its production scheduler.
  class Scheduler {
    callbacks = [];
    callbacksCount = 0;
    nextId = 1;
    startId = 1;
    isScheduled = false;
    tick = (timestamp) => {
      this.isScheduled = false;
      const currentCallbacks = this.callbacks;
      const currentCallbacksCount = this.callbacksCount;
      this.callbacks = [];
      this.callbacksCount = 0;
      this.startId = this.nextId;
      if (currentCallbacksCount > 0) {
        for (let i = 0; i < currentCallbacks.length; i += 1) {
          currentCallbacks[i]?.(timestamp);
        }
      }
    };
    request(fn) {
      const id = this.nextId;
      this.nextId += 1;
      this.callbacks.push(fn);
      this.callbacksCount += 1;
      if (!this.isScheduled) {
        requestAnimationFrame(this.tick);
        this.isScheduled = true;
      }
      return id;
    }
    cancel(id) {
      const index = id - this.startId;
      if (index < 0 || index >= this.callbacks.length || this.callbacks[index] === null) return;
      this.callbacks[index] = null;
      this.callbacksCount -= 1;
    }
  }
  const scheduler = new Scheduler();
  class AnimationFrame {
    static create() { return new AnimationFrame(); }
    static request(fn) { return scheduler.request(fn); }
    static cancel(id) { scheduler.cancel(id); }
    currentId = null;
    request(fn) {
      this.cancel();
      this.currentId = scheduler.request(() => {
        this.currentId = null;
        fn();
      });
    }
    cancel = () => {
      if (this.currentId !== null) {
        scheduler.cancel(this.currentId);
        this.currentId = null;
      }
    };
  }

  let originalHtmlStyles = {};
  let originalBodyStyles = {};
  let originalHtmlScrollBehavior = '';

  // The viewport's overflow comes from <html> when it establishes its own scroll container, and
  // propagates from <body> otherwise. An `overflow` style on the other element doesn't lock the page.
  function getViewportScroller(html, body) {
    return isOverflowElement(html) ? html : body;
  }

  function isPageScrollLocked(win, html, body) {
    return /hidden|clip/.test(win.getComputedStyle(getViewportScroller(html, body)).overflowY);
  }

  function hasInsetScrollbars(referenceElement) {
    if (typeof document === 'undefined') {
      return false;
    }
    const doc = ownerDocument(referenceElement);
    const win = ownerWindow(doc);
    return win.innerWidth - doc.documentElement.clientWidth > 0;
  }

  function supportsStableScrollbarGutter(referenceElement) {
    const supported =
      typeof CSS !== 'undefined' && CSS.supports && CSS.supports('scrollbar-gutter', 'stable');

    if (!supported || typeof document === 'undefined') {
      return false;
    }

    const doc = ownerDocument(referenceElement);
    const html = doc.documentElement;
    const body = doc.body;

    const scrollContainer = getViewportScroller(html, body);

    const originalScrollContainerOverflowY = scrollContainer.style.overflowY;
    const originalHtmlStyleGutter = html.style.scrollbarGutter;

    html.style.scrollbarGutter = 'stable';

    scrollContainer.style.overflowY = 'scroll';
    const before = scrollContainer.offsetWidth;

    scrollContainer.style.overflowY = 'hidden';
    const after = scrollContainer.offsetWidth;

    scrollContainer.style.overflowY = originalScrollContainerOverflowY;
    html.style.scrollbarGutter = originalHtmlStyleGutter;

    return before === after;
  }

  function preventScrollOverlayScrollbars(referenceElement) {
    const doc = ownerDocument(referenceElement);
    const html = doc.documentElement;
    const body = doc.body;

    // If an `overflow` style is present on <html>, we need to lock it, because a lock on <body>
    // won't have any effect.
    // But if <body> has an `overflow` style (like `overflow-x: hidden`), we need to lock it
    // instead, as sticky elements shift otherwise.
    const elementToLock = getViewportScroller(html, body);
    const originalElementToLockStyles = {
      overflowY: elementToLock.style.overflowY,
      overflowX: elementToLock.style.overflowX,
    };

    Object.assign(elementToLock.style, {
      overflowY: 'hidden',
      overflowX: 'hidden',
    });

    return () => {
      Object.assign(elementToLock.style, originalElementToLockStyles);
    };
  }

  function preventScrollInsetScrollbars(referenceElement) {
    const doc = ownerDocument(referenceElement);
    const html = doc.documentElement;
    const body = doc.body;
    const win = ownerWindow(html);

    let scrollTop = 0;
    let scrollLeft = 0;
    let updateGutterOnly = false;
    const resizeFrame = AnimationFrame.create();

    // Pinch-zoom in Safari causes a shift. Just don't lock scroll if there's any pinch-zoom.
    if (webkit && (win.visualViewport?.scale ?? 1) !== 1) {
      return () => {};
    }

    function lockScroll() {
      /* DOM reads: */

      const htmlStyles = win.getComputedStyle(html);
      const bodyStyles = win.getComputedStyle(body);
      const htmlScrollbarGutterValue = htmlStyles.scrollbarGutter || '';
      const hasBothEdges = htmlScrollbarGutterValue.includes('both-edges');
      const scrollbarGutterValue = hasBothEdges ? 'stable both-edges' : 'stable';

      scrollTop = html.scrollTop;
      scrollLeft = html.scrollLeft;

      originalHtmlStyles = {
        scrollbarGutter: html.style.scrollbarGutter,
        overflowY: html.style.overflowY,
        overflowX: html.style.overflowX,
      };
      originalHtmlScrollBehavior = html.style.scrollBehavior;

      originalBodyStyles = {
        position: body.style.position,
        height: body.style.height,
        width: body.style.width,
        boxSizing: body.style.boxSizing,
        overflowY: body.style.overflowY,
        overflowX: body.style.overflowX,
        scrollBehavior: body.style.scrollBehavior,
      };

      const isScrollableY = html.scrollHeight > html.clientHeight;
      const isScrollableX = html.scrollWidth > html.clientWidth;
      const hasConstantOverflowY =
        htmlStyles.overflowY === 'scroll' || bodyStyles.overflowY === 'scroll';
      const hasConstantOverflowX =
        htmlStyles.overflowX === 'scroll' || bodyStyles.overflowX === 'scroll';

      // Values can be negative in Firefox
      const scrollbarWidth = Math.max(0, win.innerWidth - body.clientWidth);
      const scrollbarHeight = Math.max(0, win.innerHeight - body.clientHeight);

      // Avoid shift due to the default <body> margin. This does cause elements to be clipped
      // with whitespace. Warn if <body> has margins?
      const marginY = parseFloat(bodyStyles.marginTop) + parseFloat(bodyStyles.marginBottom);
      const marginX = parseFloat(bodyStyles.marginLeft) + parseFloat(bodyStyles.marginRight);
      const elementToLock = getViewportScroller(html, body);

      updateGutterOnly = supportsStableScrollbarGutter(referenceElement);

      /*
       * DOM writes:
       * Do not read the DOM past this point!
       */

      if (updateGutterOnly) {
        html.style.scrollbarGutter = scrollbarGutterValue;
        elementToLock.style.overflowY = 'hidden';
        elementToLock.style.overflowX = 'hidden';
        return;
      }

      Object.assign(html.style, {
        scrollbarGutter: scrollbarGutterValue,
        overflowY: 'hidden',
        overflowX: 'hidden',
      });

      if (isScrollableY || hasConstantOverflowY) {
        html.style.overflowY = 'scroll';
      }
      if (isScrollableX || hasConstantOverflowX) {
        html.style.overflowX = 'scroll';
      }

      Object.assign(body.style, {
        position: 'relative',
        height:
          marginY || scrollbarHeight ? `calc(100dvh - ${marginY + scrollbarHeight}px)` : '100dvh',
        width: marginX || scrollbarWidth ? `calc(100vw - ${marginX + scrollbarWidth}px)` : '100vw',
        boxSizing: 'border-box',
        // Assign the longhands that `cleanup` restores, so nothing is left behind.
        overflowY: 'hidden',
        overflowX: 'hidden',
        scrollBehavior: 'unset',
      });

      body.scrollTop = scrollTop;
      body.scrollLeft = scrollLeft;
      html.setAttribute('data-base-ui-scroll-locked', '');
      html.style.scrollBehavior = 'unset';
    }

    function cleanup() {
      Object.assign(html.style, originalHtmlStyles);
      Object.assign(body.style, originalBodyStyles);

      if (!updateGutterOnly) {
        html.scrollTop = scrollTop;
        html.scrollLeft = scrollLeft;
        html.removeAttribute('data-base-ui-scroll-locked');
        html.style.scrollBehavior = originalHtmlScrollBehavior;
      }
    }

    function handleResize() {
      cleanup();
      resizeFrame.request(lockScroll);
    }

    lockScroll();
    win.addEventListener('resize', handleResize);

    return () => {
      resizeFrame.cancel();
      cleanup();
      // Sometimes this cleanup can run after test teardown because it is called
      // in a `setTimeout(fn, 0)`. Guard the returned cleanup to avoid calling
      // `removeEventListener` when it is no longer available in tests.
      if (typeof win.removeEventListener === 'function') {
        win.removeEventListener('resize', handleResize);
      }
    };
  }

  class ScrollLocker {
    lockCount = 0;
    restore = null;
    timeoutLock = Timeout.create();
    timeoutUnlock = Timeout.create();

    acquire(referenceElement) {
      this.lockCount += 1;
      if (this.lockCount === 1 && this.restore === null) {
        this.timeoutLock.start(0, () => this.lock(referenceElement));
      }
      return this.release;
    }

    release = () => {
      this.lockCount -= 1;
      if (this.lockCount === 0 && this.restore) {
        this.timeoutUnlock.start(0, this.unlock);
      }
    };

    unlock = () => {
      if (this.lockCount === 0 && this.restore) {
        this.restore?.();
        this.restore = null;
      }
    };

    lock(referenceElement) {
      if (this.lockCount === 0 || this.restore !== null) {
        return;
      }

      const doc = ownerDocument(referenceElement);
      const html = doc.documentElement;
      const body = doc.body;
      const win = ownerWindow(html);

      // The page is already locked, either by the site author or by a non-Base UI overlay that
      // hasn't cleaned up yet. Leave it alone and wait for the lock to clear before taking over,
      // otherwise we'd snapshot the locked state and restore it after our own lock is released.
      if (isPageScrollLocked(win, html, body)) {
        const observer = new win.MutationObserver(() => {
          if (isPageScrollLocked(win, html, body)) {
            return;
          }
          observer.disconnect();
          this.restore = null;
          this.lock(referenceElement);
        });

        // Watch every attribute: locks are applied through inline styles, classes, or attributes
        // paired with a stylesheet (`data-scroll-locked` in react-remove-scroll, for example).
        const options = { attributes: true };

        observer.observe(html, options);
        observer.observe(body, options);

        this.restore = () => observer.disconnect();
        return;
      }

      const hasOverlayScrollbars = ios || !hasInsetScrollbars(referenceElement);

      // On iOS, scroll locking does not work if the navbar is collapsed. Due to numerous
      // side effects and bugs that arise on iOS, it must be researched extensively before
      // being enabled to ensure it doesn't cause the following issues:
      // - Textboxes must scroll into view when focused, nor cause a glitchy scroll animation.
      // - The navbar must not force itself into view and cause layout shift.
      // - Scroll containers must not flicker upon closing a popup when it has an exit animation.
      this.restore = hasOverlayScrollbars
        ? preventScrollOverlayScrollbars(referenceElement)
        : preventScrollInsetScrollbars(referenceElement);
    }
  }

  const SCROLL_LOCKER = new ScrollLocker();

  // @base-ui/react/utils/useAnchoredPopupScrollLock.ts: run after positioning.
  const VIEWPORT_WIDTH_TOLERANCE_PX = 20;
  function anchoredPopupScrollLock(enabled, touchOpen, positionerElement, referenceElement) {
    let touchOpenShouldLockScroll = false;
    if (enabled && touchOpen && positionerElement != null) {
      const viewportWidth = ownerDocument(positionerElement).documentElement.clientWidth;
      const popupWidth = positionerElement.offsetWidth;
      touchOpenShouldLockScroll = viewportWidth > 0 && popupWidth > 0 &&
        popupWidth >= viewportWidth - VIEWPORT_WIDTH_TOLERANCE_PX;
    }
    return enabled && (!touchOpen || touchOpenShouldLockScroll)
      ? SCROLL_LOCKER.acquire(referenceElement)
      : () => {};
  }

  window.templ = window.templ || {};
  window.templ.scrollLock = {
    acquire: (referenceElement) => SCROLL_LOCKER.acquire(referenceElement),
    anchoredPopup: anchoredPopupScrollLock,
  };
})();

// components/baseui/tabbable.js
// Port of @base-ui/react floating-ui-react/utils/tabbable.ts (1.6.0), Base UI's
// own tabbable implementation, with activeElement and contains from
// utils/element.ts and isElementVisible from utils/composite.ts.
(function () {
  "use strict";

  const CANDIDATE_SELECTOR = 'a[href],button,input,select,textarea,summary,details,iframe,object,embed,[tabindex],[contenteditable]:not([contenteditable="false"]),audio[controls],video[controls]';

  const nodeName = (element) => (element?.nodeName || "").toLowerCase();
  const isShadowRoot = (node) => typeof ShadowRoot !== "undefined" && node instanceof ShadowRoot;
  const isHTMLElement = (node) => node instanceof HTMLElement;

  // The focused element, through open shadow roots.
  function activeElement(doc) {
    let element = doc.activeElement;
    while (element?.shadowRoot?.activeElement != null) element = element.shadowRoot.activeElement;
    return element;
  }

  function contains(parent, child) {
    if (!parent || !child) return false;
    const rootNode = child.getRootNode?.();
    if (parent.contains(child)) return true;
    if (rootNode && isShadowRoot(rootNode)) {
      let next = child;
      while (next) {
        if (parent === next) return true;
        next = next.parentNode || next.host;
      }
    }
    return false;
  }

  function isHiddenByStyles(styles) {
    return styles.visibility === "hidden" || styles.visibility === "collapse";
  }

  function isElementVisible(element, styles = element ? getComputedStyle(element) : null) {
    if (!element || !element.isConnected || !styles || isHiddenByStyles(styles)) return false;
    if (typeof element.checkVisibility === "function") return element.checkVisibility();
    return styles.display !== "none" && styles.display !== "contents";
  }

  function getParentElement(element) {
    if (element.assignedSlot) return element.assignedSlot;
    if (element.parentElement) return element.parentElement;
    const rootNode = element.getRootNode();
    return isShadowRoot(rootNode) ? rootNode.host : null;
  }

  function getDetailsSummary(details) {
    for (const child of Array.from(details.children)) {
      if (nodeName(child) === "summary") return child;
    }
    return null;
  }

  function isWithinOpenDetailsSummary(element, details) {
    const summary = getDetailsSummary(details);
    return !!summary && (element === summary || contains(summary, element));
  }

  function isFocusableCandidate(element) {
    const name = element ? nodeName(element) : "";
    return element != null && element.matches(CANDIDATE_SELECTOR) &&
      (name !== "summary" || (element.parentElement != null && nodeName(element.parentElement) === "details" && getDetailsSummary(element.parentElement) === element)) &&
      (name !== "details" || getDetailsSummary(element) == null) &&
      (name !== "input" || element.type !== "hidden");
  }

  function isVisibleInTabbableTree(element, isAncestor) {
    const styles = getComputedStyle(element);
    if (!isAncestor) return isElementVisible(element, styles);
    return styles.display !== "none";
  }

  function isFocusableElement(element) {
    if (!isFocusableCandidate(element) || !element.isConnected || element.matches(":disabled")) return false;
    for (let current = element; current; current = getParentElement(current)) {
      const isAncestor = current !== element;
      const isSlot = nodeName(current) === "slot";
      if (current.hasAttribute("inert")) return false;
      if ((isAncestor && nodeName(current) === "details" && !current.open && !isWithinOpenDetailsSummary(element, current)) ||
        current.hasAttribute("hidden") || (!isSlot && !isVisibleInTabbableTree(current, isAncestor))) {
        return false;
      }
    }
    return true;
  }

  function getTabIndex(element) {
    const tabIndex = element.tabIndex;
    if (tabIndex < 0) {
      const name = nodeName(element);
      if (name === "details" || name === "audio" || name === "video" || (isHTMLElement(element) && element.isContentEditable)) return 0;
    }
    return tabIndex;
  }

  function getNamedRadioInput(element) {
    if (nodeName(element) !== "input") return null;
    return element.type === "radio" && element.name !== "" ? element : null;
  }

  function isTabbableRadio(element, candidates) {
    const input = getNamedRadioInput(element);
    if (!input) return true;
    const checkedRadio = candidates.find((candidate) => {
      const radio = getNamedRadioInput(candidate);
      return radio?.name === input.name && radio.form === input.form && radio.checked;
    });
    if (checkedRadio) return checkedRadio === input;
    return candidates.find((candidate) => {
      const radio = getNamedRadioInput(candidate);
      return radio?.name === input.name && radio.form === input.form;
    }) === input;
  }

  function getComposedChildren(container) {
    if (isHTMLElement(container) && nodeName(container) === "slot") {
      const assignedElements = container.assignedElements({ flatten: true });
      if (assignedElements.length > 0) return assignedElements;
    }
    if (isHTMLElement(container) && container.shadowRoot) return Array.from(container.shadowRoot.children);
    return Array.from(container.children);
  }

  function appendCandidates(container, list) {
    getComposedChildren(container).forEach((child) => {
      if (isFocusableCandidate(child)) list.push(child);
      appendCandidates(child, list);
    });
  }

  function appendMatchingElements(container, selector, list) {
    getComposedChildren(container).forEach((child) => {
      if (isHTMLElement(child) && child.matches(selector)) list.push(child);
      appendMatchingElements(child, selector, list);
    });
  }

  function isTabbable(element) {
    return isFocusableElement(element) && getTabIndex(element) >= 0;
  }

  function focusable(container) {
    const candidates = [];
    appendCandidates(container, candidates);
    return candidates.filter(isFocusableElement);
  }

  function tabbable(container) {
    const candidates = focusable(container);
    return candidates.filter((element) => getTabIndex(element) >= 0 && isTabbableRadio(element, candidates));
  }

  function getTabbableIn(container, dir) {
    const list = tabbable(container);
    const len = list.length;
    if (len === 0) return undefined;
    const active = activeElement(container.ownerDocument);
    const index = list.indexOf(active);
    const nextIndex = index === -1 ? (dir === 1 ? 0 : len - 1) : index + dir;
    return list[nextIndex];
  }

  function getNextTabbable(referenceElement) {
    return getTabbableIn(referenceElement.ownerDocument.body, 1) || referenceElement;
  }

  function getPreviousTabbable(referenceElement) {
    return getTabbableIn(referenceElement.ownerDocument.body, -1) || referenceElement;
  }

  function getTabbableNearElement(referenceElement, dir) {
    if (!referenceElement) return null;
    const list = tabbable(referenceElement.ownerDocument.body);
    const elementCount = list.length;
    if (elementCount === 0) return null;
    const index = list.indexOf(referenceElement);
    if (index === -1) return null;
    return list[(index + dir + elementCount) % elementCount];
  }

  function getTabbableAfterElement(referenceElement) {
    return getTabbableNearElement(referenceElement, 1);
  }

  function getTabbableBeforeElement(referenceElement) {
    return getTabbableNearElement(referenceElement, -1);
  }

  function isOutsideEvent(event, container) {
    const containerElement = container || event.currentTarget;
    const relatedTarget = event.relatedTarget;
    return !relatedTarget || !contains(containerElement, relatedTarget);
  }

  // data-tabindex is Base UI's own marker for the tabindex it took away.
  function disableFocusInside(container) {
    tabbable(container).forEach((element) => {
      element.dataset.tabindex = element.getAttribute("tabindex") || "";
      element.setAttribute("tabindex", "-1");
    });
  }

  function enableFocusInside(container) {
    const elements = [];
    appendMatchingElements(container, "[data-tabindex]", elements);
    elements.forEach((element) => {
      const tabindex = element.dataset.tabindex;
      delete element.dataset.tabindex;
      if (tabindex) element.setAttribute("tabindex", tabindex);
      else element.removeAttribute("tabindex");
    });
  }

  window.templ = window.templ || {};
  window.templ.tabbable = {
    activeElement,
    contains,
    isElementVisible,
    isTabbable,
    focusable,
    tabbable,
    getNextTabbable,
    getPreviousTabbable,
    getTabbableAfterElement,
    getTabbableBeforeElement,
    isOutsideEvent,
    disableFocusInside,
    enableFocusInside,
  };
})();

// components/baseui/use_anchor_positioning.js
// Port of @base-ui/react utils/useAnchorPositioning.ts (1.6.0) over
// window.FloatingUIDOM from components/floatingui, with the floatingStyles of
// @floating-ui/react's useFloating.
//
// useAnchorPositioning(options) runs while the popup is mounted: it positions
// the positioner against the anchor and keeps it there (autoUpdate), and
// returns { cleanup, positioned }, where positioned resolves after the first
// position.
//
//   anchor              element, or a virtual element with getBoundingClientRect
//   positioner          the element that is positioned
//   parts               elements that render data-side and data-align (positioner, popup, arrow)
//   arrow               the arrow element, if any
//   positionMethod      "absolute" or "fixed"
//   side                top, right, bottom, left, inline-start, inline-end
//   align               start, center, end
//   sideOffset, alignOffset, collisionPadding, arrowPadding, sticky
//   collisionAvoidance  { side, align, fallbackAxisSide }
//   shiftCrossAxis, lazyFlip, disableAnchorTracking
//   inline              an extra middleware placed first, like the source
//   applyPosition       a function; while it returns false the positioner keeps
//                       its own position (select aligned with its trigger), the
//                       variables are still set
//   onPosition(result)  called after every position
//
//   adaptiveOrigin      true for popups with a viewport part (NavigationMenu):
//                       left/top (right/bottom on the top and left sides) in
//                       place of the transform, so the size and position can
//                       transition together while it stays anchored
//
// The returned setAnchor(element) moves it to another anchor without
// unpositioning it, like refs.setPositionReference.
(function () {
  "use strict";

  const getSide = (placement) => placement.split("-")[0];
  const getAlignment = (placement) => placement.split("-")[1];
  const getSideAxis = (side) => (side === "top" || side === "bottom" ? "y" : "x");

  function getLogicalSide(sideParam, renderedSide, isRtl) {
    const isLogicalSideParam = sideParam === "inline-start" || sideParam === "inline-end";
    const logicalRight = isRtl ? "inline-start" : "inline-end";
    const logicalLeft = isRtl ? "inline-end" : "inline-start";
    return {
      top: "top",
      right: isLogicalSideParam ? logicalRight : "right",
      bottom: "bottom",
      left: isLogicalSideParam ? logicalLeft : "left",
    }[renderedSide];
  }

  // DirectionProvider pendant: the nearest dir attribute.
  function isRtlAt(element) {
    const el = element?.closest ? element : element?.contextElement;
    return window.templ.direction.useDirection(el) === "rtl";
  }

  // utils/hideMiddleware.ts: an anchor with an empty rect counts as hidden too.
  function hideMiddleware() {
    const nativeHideFn = window.FloatingUIDOM.hide().fn;
    return {
      name: "hide",
      async fn(state) {
        const { width, height, x, y } = state.rects.reference;
        const anchorHidden = width === 0 && height === 0 && x === 0 && y === 0;
        const nativeHideResult = await nativeHideFn(state);
        return { data: { referenceHidden: nativeHideResult.data?.referenceHidden || anchorHidden } };
      },
    };
  }

  // floating-ui-react/middleware/arrow.ts: Base UI's fork of arrow() that
  // measures against the positioner, not the arrow's offset parent.
  function arrowMiddleware({ element, padding = 0 }) {
    return {
      name: "arrow",
      async fn(state) {
        const { x, y, placement, rects, platform, elements, middlewareData } = state;
        const paddingObject = typeof padding === "number"
          ? { top: padding, right: padding, bottom: padding, left: padding }
          : { top: 0, right: 0, bottom: 0, left: 0, ...padding };
        const coords = { x, y };
        const axis = getSideAxis(getSide(placement)) === "y" ? "x" : "y";
        const length = axis === "y" ? "height" : "width";
        const arrowDimensions = await platform.getDimensions(element);
        const isYAxis = axis === "y";
        const minProp = isYAxis ? "top" : "left";
        const maxProp = isYAxis ? "bottom" : "right";
        const clientProp = isYAxis ? "clientHeight" : "clientWidth";
        const endDiff = rects.reference[length] + rects.reference[axis] - coords[axis] - rects.floating[length];
        const startDiff = coords[axis] - rects.reference[axis];
        const clientSize = elements.floating[clientProp] || rects.floating[length];
        const centerToReference = endDiff / 2 - startDiff / 2;
        // Padding large enough to push the arrow off center is reduced.
        const largestPossiblePadding = clientSize / 2 - arrowDimensions[length] / 2 - 1;
        const minPadding = Math.min(paddingObject[minProp], largestPossiblePadding);
        const maxPadding = Math.min(paddingObject[maxProp], largestPossiblePadding);
        const min = minPadding;
        const max = clientSize - arrowDimensions[length] - maxPadding;
        const center = clientSize / 2 - arrowDimensions[length] / 2 + centerToReference;
        const offset = Math.min(Math.max(min, center), max);
        // A reference too small for an aligned arrow moves the popup itself,
        // with one reset so shift() still runs.
        const shouldAddOffset = !middlewareData.arrow && getAlignment(placement) != null && center !== offset &&
          rects.reference[length] / 2 - (center < min ? minPadding : maxPadding) - arrowDimensions[length] / 2 < 0;
        const alignmentOffset = shouldAddOffset ? (center < min ? center - min : center - max) : 0;
        return {
          [axis]: coords[axis] + alignmentOffset,
          data: {
            [axis]: offset,
            centerOffset: center - offset - alignmentOffset,
            ...(shouldAddOffset && { alignmentOffset }),
          },
          reset: shouldAddOffset,
        };
      },
    };
  }

  // @floating-ui/react roundByDPR
  function roundByDPR(element, value) {
    const dpr = element.ownerDocument.defaultView.devicePixelRatio || 1;
    return Math.round(value * dpr) / dpr;
  }

  // utils/adaptiveOriginMiddleware.ts
  const DEFAULT_SIDES = { sideX: "left", sideY: "top" };
  const adaptiveOriginMiddleware = {
    name: "adaptiveOrigin",
    async fn(state) {
      const { x: rawX, y: rawY, rects: { floating: floatRect }, elements: { floating }, platform, strategy, placement } = state;
      const win = floating.ownerDocument.defaultView;
      const styles = win.getComputedStyle(floating);
      const hasTransition = styles.transitionDuration !== "0s" && styles.transitionDuration !== "";
      if (!hasTransition) return { x: rawX, y: rawY, data: DEFAULT_SIDES };
      const offsetParent = await platform.getOffsetParent?.(floating);
      let offsetDimensions = { width: 0, height: 0 };
      if (strategy === "fixed" && win?.visualViewport) {
        offsetDimensions = { width: win.visualViewport.width, height: win.visualViewport.height };
      } else if (offsetParent === win) {
        const doc = floating.ownerDocument;
        offsetDimensions = { width: doc.documentElement.clientWidth, height: doc.documentElement.clientHeight };
      } else if (await platform.isElement?.(offsetParent)) {
        offsetDimensions = await platform.getDimensions(offsetParent);
      }
      const currentSide = getSide(placement);
      let x = rawX;
      let y = rawY;
      if (currentSide === "left") x = offsetDimensions.width - (rawX + floatRect.width);
      if (currentSide === "top") y = offsetDimensions.height - (rawY + floatRect.height);
      return {
        x,
        y,
        data: { sideX: currentSide === "left" ? "right" : DEFAULT_SIDES.sideX, sideY: currentSide === "top" ? "bottom" : DEFAULT_SIDES.sideY },
      };
    },
  };

  function useAnchorPositioning(options) {
    let {
      anchor,
    } = options;
    const {
      positioner,
      arrow: arrowEl = null,
      positionMethod = "absolute",
      side: sideParam = "bottom",
      sideOffset = 0,
      align = "center",
      alignOffset = 0,
      collisionPadding: collisionPaddingParam = 5,
      sticky = false,
      arrowPadding = 5,
      disableAnchorTracking = false,
      inline: inlineMiddleware,
      collisionAvoidance = {},
      shiftCrossAxis = false,
      lazyFlip = false,
      applyPosition = () => true,
      adaptiveOrigin = false,
      onPosition,
    } = options;
    const parts = (options.parts || [positioner]).filter(Boolean);
    const { computePosition, autoUpdate, offset, flip, shift, limitShift, size } = window.FloatingUIDOM;

    const collisionAvoidanceSide = collisionAvoidance.side || "flip";
    const collisionAvoidanceAlign = collisionAvoidance.align || "flip";
    const collisionAvoidanceFallbackAxisSide = collisionAvoidance.fallbackAxisSide || "end";
    // Base UI reads useDirection in the positioner.
    const isRtl = isRtlAt(positioner);
    let mountSide = null;

    // Create a bias to the preferred side. On iOS, when the software keyboard
    // opens, the input is exactly centered and could flip to the top.
    const bias = 1;
    const biasTop = sideParam === "bottom" ? bias : 0;
    const biasBottom = sideParam === "top" ? bias : 0;
    const biasLeft = sideParam === "right" ? bias : 0;
    const biasRight = sideParam === "left" ? bias : 0;
    const padding = typeof collisionPaddingParam === "number"
      ? { top: collisionPaddingParam, right: collisionPaddingParam, bottom: collisionPaddingParam, left: collisionPaddingParam }
      : collisionPaddingParam;
    const collisionPadding = {
      top: (padding.top || 0) + biasTop,
      right: (padding.right || 0) + biasRight,
      bottom: (padding.bottom || 0) + biasBottom,
      left: (padding.left || 0) + biasLeft,
    };
    const commonCollisionProps = { boundary: "clippingAncestors", padding: collisionPadding };

    function getOffsetData(state) {
      return {
        side: getLogicalSide(sideParam, getSide(state.placement), isRtl),
        align: getAlignment(state.placement) || "center",
        anchor: { width: state.rects.reference.width, height: state.rects.reference.height },
        positioner: { width: state.rects.floating.width, height: state.rects.floating.height },
      };
    }
    const resolveOffset = (value, state) => (typeof value === "function" ? value(getOffsetData(state)) : value);

    const shiftDisabled = collisionAvoidanceAlign === "none" && collisionAvoidanceSide !== "shift";
    const crossAxisShiftEnabled = !shiftDisabled && (sticky || shiftCrossAxis || collisionAvoidanceSide === "shift");

    function middleware() {
      const list = [];
      if (inlineMiddleware) list.push(inlineMiddleware);
      list.push(offset((state) => {
        const sideAxis = resolveOffset(sideOffset, state);
        const alignAxis = resolveOffset(alignOffset, state);
        return { mainAxis: sideAxis, crossAxis: alignAxis, alignmentAxis: alignAxis };
      }));
      const flipMiddleware = collisionAvoidanceSide === "none" ? null : flip({
        ...commonCollisionProps,
        // The popup flips once it was limited by its --available-height and
        // resizes, since the size() padding is smaller than this one.
        padding: {
          top: collisionPadding.top + bias,
          right: collisionPadding.right + bias,
          bottom: collisionPadding.bottom + bias,
          left: collisionPadding.left + bias,
        },
        mainAxis: !shiftCrossAxis && collisionAvoidanceSide === "flip",
        crossAxis: collisionAvoidanceAlign === "flip" ? "alignment" : false,
        fallbackAxisSideDirection: collisionAvoidanceFallbackAxisSide,
      });
      const shiftMiddleware = shiftDisabled ? null : shift((data) => {
        const html = data.elements.floating.ownerDocument.documentElement;
        return {
          ...commonCollisionProps,
          // The layout viewport, so pinch zooming does not shift context menus.
          rootBoundary: shiftCrossAxis ? { x: 0, y: 0, width: html.clientWidth, height: html.clientHeight } : undefined,
          mainAxis: collisionAvoidanceAlign !== "none",
          crossAxis: crossAxisShiftEnabled,
          limiter: sticky || shiftCrossAxis ? undefined : limitShift((limitData) => {
            if (!arrowEl) return {};
            const { width, height } = arrowEl.getBoundingClientRect();
            const sideAxis = getSideAxis(getSide(limitData.placement));
            const arrowSize = sideAxis === "y" ? width : height;
            const offsetAmount = sideAxis === "y"
              ? collisionPadding.left + collisionPadding.right
              : collisionPadding.top + collisionPadding.bottom;
            return { offset: arrowSize / 2 + offsetAmount / 2 };
          }),
        };
      });
      // https://floating-ui.com/docs/flip#combining-with-shift
      if (collisionAvoidanceSide === "shift" || collisionAvoidanceAlign === "shift" || align === "center") {
        list.push(shiftMiddleware, flipMiddleware);
      } else {
        list.push(flipMiddleware, shiftMiddleware);
      }
      list.push(
        size({
          ...commonCollisionProps,
          apply({ elements: { floating }, availableWidth, availableHeight, rects }) {
            const style = floating.style;
            style.setProperty("--available-width", `${availableWidth}px`);
            style.setProperty("--available-height", `${availableHeight}px`);
            // Snap the anchor size to device pixels, so the popup's visual
            // width matches the anchor's.
            const dpr = floating.ownerDocument.defaultView.devicePixelRatio || 1;
            const { x, y, width, height } = rects.reference;
            const anchorWidth = (Math.round((x + width) * dpr) - Math.round(x * dpr)) / dpr;
            const anchorHeight = (Math.round((y + height) * dpr) - Math.round(y * dpr)) / dpr;
            style.setProperty("--anchor-width", `${anchorWidth}px`);
            style.setProperty("--anchor-height", `${anchorHeight}px`);
          },
        }),
        // transform-origin relies on an arrow element, a detached one when
        // the popup has none.
        arrowMiddleware({ element: arrowEl || document.createElement("div"), padding: arrowPadding }),
        {
          name: "transformOrigin",
          fn(state) {
            const { elements, middlewareData, placement: renderedPlacement, rects, y } = state;
            const currentRenderedSide = getSide(renderedPlacement);
            const currentRenderedAxis = getSideAxis(currentRenderedSide);
            const arrowX = middlewareData.arrow?.x || 0;
            const arrowY = middlewareData.arrow?.y || 0;
            const arrowWidth = arrowEl?.clientWidth || 0;
            const arrowHeight = arrowEl?.clientHeight || 0;
            const transformX = arrowX + arrowWidth / 2;
            const transformY = arrowY + arrowHeight / 2;
            const shiftY = Math.abs(middlewareData.shift?.y || 0);
            const halfAnchorHeight = rects.reference.height / 2;
            const sideOffsetValue = resolveOffset(sideOffset, state);
            const isOverlappingAnchor = shiftY > sideOffsetValue;
            const adjacentTransformOrigin = {
              top: `${transformX}px calc(100% + ${sideOffsetValue}px)`,
              bottom: `${transformX}px ${-sideOffsetValue}px`,
              left: `calc(100% + ${sideOffsetValue}px) ${transformY}px`,
              right: `${-sideOffsetValue}px ${transformY}px`,
            }[currentRenderedSide];
            const overlapTransformOrigin = `${transformX}px ${rects.reference.y + halfAnchorHeight - y}px`;
            elements.floating.style.setProperty(
              "--transform-origin",
              crossAxisShiftEnabled && currentRenderedAxis === "y" && isOverlappingAnchor
                ? overlapTransformOrigin
                : adjacentTransformOrigin,
            );
            return {};
          },
        },
        hideMiddleware(),
        adaptiveOrigin ? adaptiveOriginMiddleware : null,
      );
      return list.filter(Boolean);
    }

    function placement() {
      const side = mountSide || {
        top: "top",
        right: "right",
        bottom: "bottom",
        left: "left",
        "inline-end": isRtl ? "left" : "right",
        "inline-start": isRtl ? "right" : "left",
      }[sideParam];
      return align === "center" ? side : `${side}-${align}`;
    }

    // Not positioned yet: fixed, so autoFocus does not scroll, and invisible.
    positioner.style.position = "fixed";
    positioner.style.opacity = "0";

    let active = true;
    let resolvePositioned;
    const positioned = new Promise((resolve) => {
      resolvePositioned = resolve;
    });

    function update() {
      return computePosition(anchor, positioner, {
        placement: placement(),
        strategy: positionMethod,
        middleware: middleware(),
      }).then((result) => {
        if (!active) return;
        const style = positioner.style;
        const renderedSide = getSide(result.placement);
        const renderedAlign = getAlignment(result.placement) || "center";
        const logicalSide = getLogicalSide(sideParam, renderedSide, isRtl);
        style.opacity = "";
        if (applyPosition()) {
          style.position = positionMethod;
          if (adaptiveOrigin) {
            // { position, [sideX]: x, [sideY]: y }
            const { sideX, sideY } = result.middlewareData.adaptiveOrigin || DEFAULT_SIDES;
            ["left", "top", "right", "bottom"].forEach((prop) => {
              if (prop !== sideX && prop !== sideY) style[prop] = "";
            });
            style[sideX] = `${result.x}px`;
            style[sideY] = `${result.y}px`;
          } else {
            // floatingStyles of useFloating with transform: true
            style.left = "0px";
            style.top = "0px";
            style.transform = `translate(${roundByDPR(positioner, result.x)}px, ${roundByDPR(positioner, result.y)}px)`;
            if ((positioner.ownerDocument.defaultView.devicePixelRatio || 1) >= 1.5) style.willChange = "transform";
          }
          parts.forEach((part) => part.setAttribute("data-side", logicalSide));
        }
        parts.forEach((part) => part.setAttribute("data-align", renderedAlign));
        positioner.toggleAttribute("data-anchor-hidden", !!result.middlewareData.hide?.referenceHidden);
        if (arrowEl) {
          arrowEl.style.position = "absolute";
          arrowEl.style.left = result.middlewareData.arrow?.x != null ? `${result.middlewareData.arrow.x}px` : "";
          arrowEl.style.top = result.middlewareData.arrow?.y != null ? `${result.middlewareData.arrow.y}px` : "";
          arrowEl.toggleAttribute("data-uncentered", result.middlewareData.arrow?.centerOffset !== 0);
        }
        // lazyFlip locks the side once positioned, so a filtered list that
        // resizes does not flip back and forth.
        if (lazyFlip) mountSide = renderedSide;
        onPosition?.(result, logicalSide);
        resolvePositioned();
      });
    }

    const autoUpdateOptions = {
      elementResize: !disableAnchorTracking && typeof ResizeObserver !== "undefined",
      layoutShift: !disableAnchorTracking && typeof IntersectionObserver !== "undefined",
    };
    let stopAutoUpdate = autoUpdate(anchor, positioner, update, autoUpdateOptions);

    return {
      positioned,
      update,
      setAnchor(next) {
        if (!active || next === anchor) return;
        stopAutoUpdate();
        anchor = next;
        stopAutoUpdate = autoUpdate(anchor, positioner, update, autoUpdateOptions);
      },
      cleanup() {
        active = false;
        stopAutoUpdate();
        resolvePositioned();
      },
    };
  }

  window.templ = window.templ || {};
  window.templ.anchorPositioning = { useAnchorPositioning };
})();

// components/baseui/use_click.js
// Port of @base-ui/react floating-ui-react/hooks/useClick.ts (1.6.0).
// Opens or closes a popup when its trigger is pressed.
//
//   const cleanup = window.templ.click.useClick(trigger, options)
//
//   event           "click" (default), "mousedown", or "mousedown-only"
//   toggle          a repeated press closes (default true), or a function
//                   read on every press
//   ignoreMouse     mouse presses do nothing (default false)
//   stickIfOpen     a popup opened by hover or focus stays on a click (default
//                   true), or a function read on every press
//   touchOpenDelay  ms before a touch opens (default 0)
//   isOpen()        whether the popup is open
//   isActiveTrigger()  whether this trigger opened it, default true
//   openEventType() the type of the event that opened it, if any
//   onOpenChange(open, event, pointerType)  requests the change
//
// With "mousedown" the open waits one frame, until the browser moved focus,
// instead of preventing the default, which would show :focus-visible.
(function () {
  "use strict";

  const TYPEABLE_SELECTOR = "input:not([type='hidden']):not([disabled]),[contenteditable]:not([contenteditable='false']),textarea:not([disabled])";
  const isMouseLikePointerType = (pointerType) => pointerType === "mouse" || pointerType === "pen";

  function useClick(trigger, options) {
    const {
      event: eventOption = "click",
      toggle = true,
      ignoreMouse = false,
      stickIfOpen = true,
      touchOpenDelay = 0,
      isOpen,
      isActiveTrigger = () => true,
      openEventType = () => null,
      onOpenChange,
    } = options;
    const read = (value) => (typeof value === "function" ? value() : value);
    let pointerType;
    let frame = 0;
    let touchOpenTimer = 0;

    function setOpenWithTouchDelay(nextOpen, event, type) {
      if (nextOpen && type === "touch" && touchOpenDelay > 0) {
        clearTimeout(touchOpenTimer);
        touchOpenTimer = setTimeout(() => onOpenChange(true, event, type), touchOpenDelay);
      } else {
        onOpenChange(nextOpen, event, type);
      }
    }

    function getNextOpen(isClickLikeOpenEvent) {
      const open = isOpen();
      // Moving between triggers always opens the newly active one.
      if (open && !isActiveTrigger()) return true;
      // A closed popup opens on the next press.
      if (!open) return true;
      // Non toggle mode never closes on a repeated press.
      if (!read(toggle)) return true;
      // A popup opened by hover or focus stays until a click like event closes it.
      const openType = openEventType();
      if (openType && read(stickIfOpen)) return !isClickLikeOpenEvent(openType);
      return false;
    }

    function onPointerDown(event) {
      pointerType = event.pointerType;
    }

    function onMouseDown(event) {
      const type = pointerType;
      if (event.button !== 0 || eventOption === "click" || (isMouseLikePointerType(type) && ignoreMouse)) return;
      const nextOpen = getNextOpen((openType) => openType === "click" || openType === "mousedown");
      // Focus is always set on typeable elements, which open at once.
      if (event.target instanceof Element && event.target.matches(TYPEABLE_SELECTOR)) {
        setOpenWithTouchDelay(nextOpen, event, type);
        return;
      }
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => setOpenWithTouchDelay(nextOpen, event, type));
    }

    function onClick(event) {
      if (eventOption === "mousedown-only") return;
      const type = pointerType;
      if (eventOption === "mousedown" && type) {
        pointerType = undefined;
        return;
      }
      if (isMouseLikePointerType(type) && ignoreMouse) return;
      const nextOpen = getNextOpen((openType) => ["click", "mousedown", "keydown", "keyup"].includes(openType));
      setOpenWithTouchDelay(nextOpen, event, type);
    }

    function onKeyDown() {
      pointerType = undefined;
    }

    trigger.addEventListener("pointerdown", onPointerDown);
    trigger.addEventListener("mousedown", onMouseDown);
    trigger.addEventListener("click", onClick);
    trigger.addEventListener("keydown", onKeyDown);
    return () => {
      cancelAnimationFrame(frame);
      clearTimeout(touchOpenTimer);
      trigger.removeEventListener("pointerdown", onPointerDown);
      trigger.removeEventListener("mousedown", onMouseDown);
      trigger.removeEventListener("click", onClick);
      trigger.removeEventListener("keydown", onKeyDown);
    };
  }

  window.templ = window.templ || {};
  window.templ.click = { useClick };
})();

// components/baseui/use_dismiss.js
// Port of @base-ui/react floating-ui-react/hooks/useDismiss.ts (1.6.0).
// Closes an open popup on Escape and on a press outside of it.
//
// useDismiss(options) runs while the popup is open, like the source's effect:
// call it on open and call the cleanup it returns on close.
//
//   floating           the popup element (required)
//   reference          the trigger element, or a list of them (the input for a combobox)
//   onOpenChange(open, reason, event)  requests the close, returns true when accepted
//   escapeKey          true, or a function read on every key press
//   outsidePress       true, false, or a function(event) that returns whether to dismiss
//   outsidePressEvent  "sloppy" (press), "intentional" (click), { mouse, touch }, or a function
//   referencePress     false, true, or a function: a press on the reference closes
//   bubbles            true, or { escapeKey, outsidePress }
//
// What differs from the source comes from having no React tree:
// - Base UI marks events inside its React tree, portals included. Here that tree
//   is the DOM plus the portal owners from portal.js, see withinTree.
// - The floating tree is every open useDismiss: a child is one whose popup lies
//   inside this popup's tree.
// - Options React re-reads on every render may be functions, read per event.
(function () {
  "use strict";

  const webkit = typeof CSS !== "undefined" && !!CSS.supports?.("-webkit-backdrop-filter:none");

  // Open instances, the FloatingTree pendant.
  const instances = new Set();

  // IME composition: Escape that settles a composition closes the compose
  // menu, not the popup. Safari fires compositionend before keydown.
  let isComposing = false;
  let compositionTimer = 0;
  document.addEventListener("compositionstart", () => {
    clearTimeout(compositionTimer);
    isComposing = true;
  });
  document.addEventListener("compositionend", () => {
    compositionTimer = setTimeout(() => {
      isComposing = false;
    }, webkit ? 5 : 0);
  });

  function getTarget(event) {
    return event.composedPath?.()[0] || event.target;
  }

  function contains(parent, child) {
    return !!parent && !!child && (parent === child || parent.contains(child));
  }

  // The React tree pendant: up through the DOM, and from a portaled element
  // on to the place it was declared.
  function withinTree(root, target) {
    for (let node = target; node; node = window.templ.portal.treeParent(node)) {
      if (node === root) return true;
    }
    return false;
  }

  function isLastTraversableNode(node) {
    return ["html", "body", "#document"].includes(node.nodeName.toLowerCase());
  }

  function normalizeBubbles(bubbles) {
    return {
      escapeKey: typeof bubbles === "boolean" ? bubbles : bubbles?.escapeKey ?? false,
      outsidePress: typeof bubbles === "boolean" ? bubbles : bubbles?.outsidePress ?? true,
    };
  }

  function useDismiss(options) {
    const {
      floating,
      onOpenChange,
      escapeKey = true,
      outsidePress = true,
      outsidePressEvent = "sloppy",
      referencePress = false,
    } = options;
    const { reference } = options;
    const references = (reference instanceof Element ? [reference] : [...(reference || [])]).filter(Boolean);
    const bubbles = normalizeBubbles(options.bubbles);
    const self = { floating, bubbles, active: true };

    let pressStartedInside = false;
    let pressStartPrevented = false;
    // Ignore only the very next outside click after dragging from inside to outside.
    let suppressNextOutsideClick = false;
    let currentPointerType = "";
    let touchState = null;
    let cancelDismissOnEndTimer = 0;
    let preventedPressSuppressionTimer = 0;

    const read = (value) => (typeof value === "function" ? value() : value);

    function hasBlockingChild(bubbleKey) {
      for (const other of instances) {
        if (other !== self && withinTree(floating, other.floating) && !other.bubbles[bubbleKey]) return true;
      }
      return false;
    }

    function isEventWithinOwnElements(event) {
      const target = getTarget(event);
      return contains(floating, target) || references.some((reference) => contains(reference, target));
    }

    function close(reason, event) {
      return self.active && onOpenChange(false, reason, event);
    }

    function closeOnEscapeKeyDown(event) {
      if (!self.active || !read(escapeKey) || event.key !== "Escape") return;
      if (isComposing) return;
      if (!bubbles.escapeKey && hasBlockingChild("escapeKey")) return;
      if (close("escape-key", event)) event.preventDefault();
      if (!bubbles.escapeKey) event.stopPropagation();
    }

    // The popup's own press handlers (the source's floating props).
    function markPressStartedInside(event) {
      if (event.button !== 0) return;
      // Only presses that start in the popup's DOM subtree count as inside,
      // so a nested portal does not suppress the parent's dismissal.
      if (!contains(floating, getTarget(event))) return;
      if (!pressStartedInside) {
        pressStartedInside = true;
        pressStartPrevented = false;
      }
    }

    function markInsidePressStartPrevented(event) {
      if (event.defaultPrevented && pressStartedInside) pressStartPrevented = true;
    }

    function getOutsidePressEvent() {
      const type = currentPointerType === "pen" || !currentPointerType ? "mouse" : currentPointerType;
      const value = read(outsidePressEvent);
      return typeof value === "string" ? value : value[type];
    }

    function shouldIgnoreEvent(event) {
      const mode = getOutsidePressEvent();
      return (mode === "intentional" && event.type !== "click") || (mode === "sloppy" && event.type === "click");
    }

    function suppressImmediateOutsideClickAfterPreventedStart() {
      suppressNextOutsideClick = true;
      clearTimeout(preventedPressSuppressionTimer);
      preventedPressSuppressionTimer = setTimeout(() => {
        suppressNextOutsideClick = false;
      }, 0);
    }

    function resetPressStartState() {
      pressStartedInside = false;
      pressStartPrevented = false;
    }

    function closeOnPressOutside(event) {
      if (!self.active) return;
      if (shouldIgnoreEvent(event)) {
        // A new press began outside the popup and its trigger. Clear any
        // leftover drag-out suppression so this press's click can dismiss.
        if (event.type !== "click" && !isEventWithinOwnElements(event)) {
          clearTimeout(preventedPressSuppressionTimer);
          suppressNextOutsideClick = false;
        }
        return;
      }
      const target = getTarget(event);
      // Inside the popup's tree, portaled children included.
      if (withinTree(floating, target)) return;
      // Another trigger of this popup was pressed.
      if (references.some((reference) => contains(reference, target))) return;

      // A press on a third party element injected after the popup opened:
      // its top level ancestor carries none of the data-base-ui-inert markers
      // markOthers set when it opened.
      const targetRoot = target instanceof Element ? target.getRootNode() : null;
      const isShadowRoot = typeof ShadowRoot !== "undefined" && targetRoot instanceof ShadowRoot;
      const markers = Array.from((isShadowRoot ? targetRoot : floating.ownerDocument).querySelectorAll("[data-base-ui-inert]"));
      let targetRootAncestor = target instanceof Element ? target : null;
      while (targetRootAncestor && !isLastTraversableNode(targetRootAncestor)) {
        const nextParent = targetRootAncestor.assignedSlot || targetRootAncestor.parentNode ||
          (targetRootAncestor.parentNode instanceof ShadowRoot ? targetRootAncestor.parentNode.host : null);
        if (!nextParent || isLastTraversableNode(nextParent) || !(nextParent instanceof Element)) break;
        targetRootAncestor = nextParent;
      }
      if (markers.length && target instanceof Element && !target.matches("html,body") &&
        !contains(target, floating) && markers.every((marker) => !contains(targetRootAncestor, marker))) {
        return;
      }

      // A press on a scrollbar. Touch never hits a scrollbar.
      if (target instanceof HTMLElement && !("touches" in event)) {
        const lastTraversableNode = isLastTraversableNode(target);
        const style = getComputedStyle(target);
        const scrollRe = /auto|scroll/;
        const isScrollableX = lastTraversableNode || scrollRe.test(style.overflowX);
        const isScrollableY = lastTraversableNode || scrollRe.test(style.overflowY);
        const canScrollX = isScrollableX && target.clientWidth > 0 && target.scrollWidth > target.clientWidth;
        const canScrollY = isScrollableY && target.clientHeight > 0 && target.scrollHeight > target.clientHeight;
        const isRTL = style.direction === "rtl";
        const pressedVerticalScrollbar = canScrollY &&
          (isRTL ? event.offsetX <= target.offsetWidth - target.clientWidth : event.offsetX > target.clientWidth);
        const pressedHorizontalScrollbar = canScrollX && event.offsetY > target.clientHeight;
        if (pressedVerticalScrollbar || pressedHorizontalScrollbar) return;
      }

      // In intentional mode, a press that starts inside and ends outside gets
      // one suppressed outside click.
      if (getOutsidePressEvent() === "intentional" && suppressNextOutsideClick) {
        clearTimeout(preventedPressSuppressionTimer);
        suppressNextOutsideClick = false;
        return;
      }
      if (typeof outsidePress === "function" && !outsidePress(event)) return;
      if (hasBlockingChild("outsidePress")) return;
      close("outside-press", event);
    }

    function handlePointerDown(event) {
      if (getOutsidePressEvent() !== "sloppy" || event.pointerType === "touch" || isEventWithinOwnElements(event)) return;
      closeOnPressOutside(event);
    }

    function handleTouchStart(event) {
      if (getOutsidePressEvent() !== "sloppy" || isEventWithinOwnElements(event)) return;
      const touch = event.touches[0];
      if (!touch) return;
      touchState = {
        startX: touch.clientX,
        startY: touch.clientY,
        dismissOnTouchEnd: false,
        dismissOnMouseDown: true,
      };
      clearTimeout(cancelDismissOnEndTimer);
      cancelDismissOnEndTimer = setTimeout(() => {
        if (touchState) {
          touchState.dismissOnTouchEnd = false;
          touchState.dismissOnMouseDown = false;
        }
      }, 1000);
    }

    // Runs the listener after the target's own handlers, in the bubble phase
    // of the target, like the source.
    function addTargetEventListenerOnce(event, listener) {
      const target = getTarget(event);
      if (!target) return;
      const once = () => {
        target.removeEventListener(event.type, once);
        listener(event);
      };
      target.addEventListener(event.type, once);
    }

    function handleTouchStartCapture(event) {
      currentPointerType = "touch";
      addTargetEventListenerOnce(event, handleTouchStart);
    }

    function closeOnPressOutsideCapture(event) {
      clearTimeout(cancelDismissOnEndTimer);
      if (event.type === "pointerdown") currentPointerType = event.pointerType;
      if (event.type === "mousedown" && touchState && !touchState.dismissOnMouseDown) return;
      addTargetEventListenerOnce(event, (targetEvent) => {
        if (targetEvent.type === "pointerdown") handlePointerDown(targetEvent);
        else closeOnPressOutside(targetEvent);
      });
    }

    function handlePressEndCapture(event) {
      if (!pressStartedInside) return;
      const startPrevented = pressStartPrevented;
      resetPressStartState();
      if (getOutsidePressEvent() !== "intentional") return;
      if (event.type === "pointercancel") {
        if (startPrevented) suppressImmediateOutsideClickAfterPreventedStart();
        return;
      }
      if (withinTree(floating, getTarget(event))) return;
      if (startPrevented) {
        suppressImmediateOutsideClickAfterPreventedStart();
        return;
      }
      if (typeof outsidePress === "function" && !outsidePress(event)) return;
      clearTimeout(preventedPressSuppressionTimer);
      suppressNextOutsideClick = true;
    }

    function handleTouchMove(event) {
      if (getOutsidePressEvent() !== "sloppy" || !touchState || isEventWithinOwnElements(event)) return;
      const touch = event.touches[0];
      if (!touch) return;
      const distance = Math.hypot(touch.clientX - touchState.startX, touch.clientY - touchState.startY);
      if (distance > 5) touchState.dismissOnTouchEnd = true;
      if (distance > 10) {
        closeOnPressOutside(event);
        clearTimeout(cancelDismissOnEndTimer);
        touchState = null;
      }
    }

    function handleTouchEnd(event) {
      if (getOutsidePressEvent() !== "sloppy" || !touchState || isEventWithinOwnElements(event)) return;
      if (touchState.dismissOnTouchEnd) closeOnPressOutside(event);
      clearTimeout(cancelDismissOnEndTimer);
      touchState = null;
    }

    // The reference's onPointerDown and onClick.
    function closeOnReferencePress(event) {
      if (!read(referencePress)) return;
      close("trigger-press", event);
    }

    const listeners = [
      ...references.map((reference) => [reference, "pointerdown", closeOnReferencePress]),
      ...references.map((reference) => [reference, "click", closeOnReferencePress]),
      [document, "keydown", closeOnEscapeKeyDown],
      ...references.map((reference) => [reference, "keydown", closeOnEscapeKeyDown]),
      [floating, "keydown", closeOnEscapeKeyDown],
      [floating, "pointerdown", markPressStartedInside, true],
      [floating, "mousedown", markPressStartedInside, true],
      [floating, "pointerdown", markInsidePressStartPrevented],
      [floating, "mousedown", markInsidePressStartPrevented],
    ];
    if (outsidePress !== false) {
      listeners.push(
        [document, "click", closeOnPressOutsideCapture, true],
        [document, "pointerdown", closeOnPressOutsideCapture, true],
        [document, "pointerup", handlePressEndCapture, true],
        [document, "pointercancel", handlePressEndCapture, true],
        [document, "mousedown", closeOnPressOutsideCapture, true],
        [document, "mouseup", handlePressEndCapture, true],
        [document, "touchstart", handleTouchStartCapture, true],
        [document, "touchmove", (event) => addTargetEventListenerOnce(event, handleTouchMove), true],
        [document, "touchend", (event) => addTargetEventListenerOnce(event, handleTouchEnd), true],
      );
    }
    listeners.forEach(([target, type, listener, capture]) => target.addEventListener(type, listener, !!capture));
    instances.add(self);

    return function cleanup() {
      if (!self.active) return;
      self.active = false;
      instances.delete(self);
      listeners.forEach(([target, type, listener, capture]) => target.removeEventListener(type, listener, !!capture));
      clearTimeout(cancelDismissOnEndTimer);
      clearTimeout(preventedPressSuppressionTimer);
    };
  }

  window.templ = window.templ || {};
  window.templ.dismiss = { useDismiss };
})();

// components/baseui/use_focus.js
// Port of @base-ui/react floating-ui-react/hooks/useFocus.ts (1.6.0). Opens
// a popup while its trigger has visible focus, like CSS :focus-visible.
//
//   const focus = window.templ.focus.useFocus(trigger, context, options)
//   focus.openChange(open, reason)   after every open change, the store's openchange
//   focus.cleanup()
//
// context is the floating root context as in use_hover.js: isOpen(),
// onOpenChange(open, reason, event), domReference(), floating(), triggers().
// Options: enabled() and delay (ms or a function).
(function () {
  "use strict";

  const t = () => window.templ.tabbable;
  const TYPEABLE_SELECTOR = "input:not([type='hidden']):not([disabled]),[contenteditable]:not([contenteditable='false']),textarea:not([disabled])";
  const isMacSafari = /Mac/.test(navigator.platform) && /AppleWebKit/.test(navigator.userAgent) && !/Chrome|Chromium|Edg/.test(navigator.userAgent);

  function matchesFocusVisible(element) {
    try {
      return element.matches(":focus-visible");
    } catch {
      return true;
    }
  }

  function isInsideEnabledTrigger(target, triggers) {
    if (!(target instanceof Element)) return false;
    const trigger = triggers.find((element) => t().contains(element, target));
    return !!trigger && !trigger.hasAttribute("data-trigger-disabled");
  }

  function useFocus(trigger, context, options = {}) {
    const { enabled = () => true, delay } = options;
    const win = trigger.ownerDocument.defaultView;
    let blockFocus = false;
    // The reference blocked from reopening after an Escape or press dismissal.
    let blockedReference = null;
    let keyboardModality = true;
    let timeout = 0;
    const startTimeout = (ms, fn) => {
      clearTimeout(timeout);
      timeout = setTimeout(fn, ms);
    };

    // Focus that stays on a closed trigger while the window loses it does
    // not open the popup when the window comes back.
    function onWindowBlur() {
      const reference = context.domReference();
      if (!context.isOpen() && reference === t().activeElement(reference.ownerDocument)) blockFocus = true;
    }
    const onKeyDown = () => {
      keyboardModality = true;
    };
    const onPointerDown = () => {
      keyboardModality = false;
    };

    function resetBlockedFocus() {
      blockFocus = false;
      blockedReference = null;
    }

    function onFocus(event) {
      if (!enabled()) return;
      const focusTarget = event.currentTarget;
      if (blockFocus) {
        if (blockedReference === focusTarget) return;
        resetBlockedFocus();
      }
      const target = event.target;
      if (target instanceof Element) {
        // Safari does not match :focus-visible when the focus came from
        // outside the document.
        if (isMacSafari && !event.relatedTarget) {
          if (!keyboardModality && !target.matches(TYPEABLE_SELECTOR)) return;
        } else if (!matchesFocusVisible(target)) {
          return;
        }
      }
      const movedFromOtherEnabledTrigger = isInsideEnabledTrigger(event.relatedTarget, context.triggers());
      const delayValue = typeof delay === "function" ? delay() : delay;
      if ((context.isOpen() && movedFromOtherEnabledTrigger) || delayValue === 0 || delayValue === undefined) {
        context.onOpenChange(true, "trigger-focus", event);
        return;
      }
      startTimeout(delayValue, () => {
        if (blockFocus) return;
        context.onOpenChange(true, "trigger-focus", event);
      });
    }

    function onBlur(event) {
      if (!enabled()) return;
      resetBlockedFocus();
      const relatedTarget = event.relatedTarget;
      // Moving to a non modal focus manager's portal guard, focus goes into
      // the popup right after.
      const movedToFocusGuard = relatedTarget instanceof Element &&
        relatedTarget.hasAttribute("data-base-ui-focus-guard") && relatedTarget.getAttribute("data-type") === "outside";
      // Waits for the window blur listener.
      startTimeout(0, () => {
        const reference = context.domReference();
        const activeEl = t().activeElement(reference.ownerDocument);
        // Focus left the page: keep it open.
        if (!relatedTarget && activeEl === reference) return;
        // A click into the popup keeps it open.
        if (t().contains(context.floating(), activeEl) || t().contains(reference, activeEl) || movedToFocusGuard) return;
        // Another trigger's focus handler takes over.
        if (isInsideEnabledTrigger(relatedTarget ?? activeEl, context.triggers())) return;
        context.onOpenChange(false, "trigger-focus", event);
      });
    }

    trigger.addEventListener("mouseleave", resetBlockedFocus);
    trigger.addEventListener("focusin", onFocus);
    trigger.addEventListener("focusout", onBlur);
    win.addEventListener("blur", onWindowBlur);
    if (isMacSafari) {
      win.addEventListener("keydown", onKeyDown, true);
      win.addEventListener("pointerdown", onPointerDown, true);
    }

    return {
      // The source's openchange listener: a close by pressing the trigger or
      // by Escape blocks the focus from reopening it.
      openChange(open, reason) {
        if (reason === "trigger-press" || reason === "escape-key") {
          blockedReference = context.domReference();
          blockFocus = true;
        }
      },
      cleanup() {
        clearTimeout(timeout);
        trigger.removeEventListener("mouseleave", resetBlockedFocus);
        trigger.removeEventListener("focusin", onFocus);
        trigger.removeEventListener("focusout", onBlur);
        win.removeEventListener("blur", onWindowBlur);
        win.removeEventListener("keydown", onKeyDown, true);
        win.removeEventListener("pointerdown", onPointerDown, true);
      },
    };
  }

  window.templ = window.templ || {};
  window.templ.focus = { useFocus };
})();

// components/baseui/use_hover.js
// Port of @base-ui/react floating-ui-react hooks/useHoverReferenceInteraction.ts,
// hooks/useHoverFloatingInteraction.ts, hooks/useHoverInteractionSharedState.ts,
// hooks/useHoverShared.ts and safePolygon.ts (1.6.0). Opens a popup while
// the pointer rests on its trigger and closes it when the pointer leaves
// both, through the safe triangle toward the popup.
//
//   const hover = window.templ.hover.createHoverInteraction(context)
//   const cleanup = window.templ.hover.useHoverReferenceInteraction(trigger, hover, options)
//   const cleanup = window.templ.hover.useHoverFloatingInteraction(hover, options)
//   hover.openChange(open, reason)   after every open change, the store's openchange
//   hover.dispose()                  when the component unmounts
//
// context is the floating root context, as functions:
//   isOpen()                      open
//   onOpenChange(open, reason, event)   store.setOpen
//   openEventType()               the type of the event that opened it, or null
//   domReference()                the active trigger
//   floating()                    the positioner while mounted, else null
//   placement()                   the rendered side, like "right"
//   triggers()                    every trigger element
//   parentFloating()              the floating element of the parent node, or null
//
// Reference options: enabled(), delay (number, { open, close } or fn),
// handleClose (safePolygon()), mouseOnly, restMs (number or fn), move,
// isClosing(), shouldOpen().
// Floating options: enabled(), closeDelay (number or fn).
//
// The floating tree is every open hover context: a child is one whose
// floating element lies inside this one's tree, see use_dismiss.js.
(function () {
  "use strict";

  const t = () => window.templ.tabbable;
  const TYPEABLE_SELECTOR = "input:not([type='hidden']):not([disabled]),[contenteditable]:not([contenteditable='false']),textarea:not([disabled])";

  // @base-ui/utils/useTimeout's Timeout.
  class Timeout {
    constructor() {
      this.id = 0;
    }
    start(delay, fn) {
      this.clear();
      this.id = setTimeout(() => {
        this.id = 0;
        fn();
      }, delay);
    }
    isStarted() {
      return this.id !== 0;
    }
    clear() {
      clearTimeout(this.id);
      this.id = 0;
    }
  }

  // The React tree pendant: up through the DOM, and from a portaled node to
  // where it was declared.
  function withinTree(root, target) {
    for (let node = target; node; node = window.templ.portal.treeParent(node)) {
      if (node === root) return true;
    }
    return false;
  }

  // ----- the floating tree ---------------------------------------------------

  const contexts = new Set();
  const closedListeners = new Set();

  // getNodeChildren: the open descendants of a floating element.
  function openChildrenOf(floating) {
    if (!floating) return [];
    return [...contexts].filter((other) => {
      const otherFloating = other.floating();
      return otherFloating && otherFloating !== floating && other.isOpen() && withinTree(floating, otherFloating);
    });
  }

  function emitClosed(event) {
    [...closedListeners].forEach((listener) => listener(event));
  }

  // ----- useHoverShared.ts ---------------------------------------------------

  const isMouseLikePointerType = (pointerType) => ["mouse", "pen", "", undefined].includes(pointerType);

  function resolveValue(value, pointerType) {
    if (pointerType != null && !isMouseLikePointerType(pointerType)) return 0;
    return typeof value === "function" ? value() : value;
  }

  function getDelay(value, prop, pointerType) {
    const result = resolveValue(value, pointerType);
    return typeof result === "number" ? result : result?.[prop];
  }

  function getRestMs(value) {
    return typeof value === "function" ? value() : value;
  }

  function isClickLikeOpenEvent(openEventType, interactedInside) {
    return interactedInside || openEventType === "click" || openEventType === "mousedown";
  }

  function isHoverOpenEvent(openEventType) {
    return !!openEventType?.includes("mouse") && openEventType !== "mousedown";
  }

  function isInsideEnabledTrigger(target, triggers) {
    if (!(target instanceof Element)) return false;
    const trigger = triggers.find((element) => t().contains(element, target));
    return !!trigger && !trigger.hasAttribute("data-trigger-disabled");
  }

  function isInteractiveElement(element) {
    return element?.closest?.(`button,a[href],[role="button"],select,[tabindex]:not([tabindex="-1"]),${TYPEABLE_SELECTOR}`) != null;
  }

  // ----- useHoverInteractionSharedState.ts -----------------------------------

  const pointerEventsMutationOwnerByScopeElement = new WeakMap();

  function clearSafePolygonPointerEventsMutation(instance) {
    if (!instance.performedPointerEventsMutation) return;
    const scopeElement = instance.pointerEventsScopeElement;
    if (scopeElement && pointerEventsMutationOwnerByScopeElement.get(scopeElement) === instance) {
      instance.pointerEventsScopeElement?.style.removeProperty("pointer-events");
      instance.pointerEventsReferenceElement?.style.removeProperty("pointer-events");
      instance.pointerEventsFloatingElement?.style.removeProperty("pointer-events");
      pointerEventsMutationOwnerByScopeElement.delete(scopeElement);
    }
    instance.performedPointerEventsMutation = false;
    instance.pointerEventsScopeElement = null;
    instance.pointerEventsReferenceElement = null;
    instance.pointerEventsFloatingElement = null;
  }

  function applySafePolygonPointerEventsMutation(instance, { scopeElement, referenceElement, floatingElement }) {
    const existingOwner = pointerEventsMutationOwnerByScopeElement.get(scopeElement);
    if (existingOwner && existingOwner !== instance) clearSafePolygonPointerEventsMutation(existingOwner);
    clearSafePolygonPointerEventsMutation(instance);
    instance.performedPointerEventsMutation = true;
    instance.pointerEventsScopeElement = scopeElement;
    instance.pointerEventsReferenceElement = referenceElement;
    instance.pointerEventsFloatingElement = floatingElement;
    pointerEventsMutationOwnerByScopeElement.set(scopeElement, instance);
    scopeElement.style.pointerEvents = "none";
    referenceElement.style.pointerEvents = "auto";
    floatingElement.style.pointerEvents = "auto";
  }

  // The HoverInteraction instance, shared by the reference and the floating
  // interaction of one popup, with the context it belongs to.
  function createHoverInteraction(context) {
    const openChangeListeners = new Set();
    const instance = {
      context,
      pointerType: undefined,
      interactedInside: false,
      handler: undefined,
      blockMouseMove: true,
      performedPointerEventsMutation: false,
      pointerEventsScopeElement: null,
      pointerEventsReferenceElement: null,
      pointerEventsFloatingElement: null,
      restTimeoutPending: false,
      openChangeTimeout: new Timeout(),
      restTimeout: new Timeout(),
      handleCloseOptions: undefined,
      onOpenChangeLocal: openChangeListeners,
      openChange(open, reason) {
        [...openChangeListeners].forEach((listener) => listener(open, reason));
      },
      dispose() {
        instance.openChangeTimeout.clear();
        instance.restTimeout.clear();
        contexts.delete(context);
      },
    };
    contexts.add(context);
    return instance;
  }

  // ----- useHoverReferenceInteraction.ts --------------------------------------

  function useHoverReferenceInteraction(trigger, instance, options = {}) {
    const {
      enabled = () => true,
      delay = 0,
      handleClose = null,
      mouseOnly = false,
      restMs = 0,
      move = true,
      isClosing,
      shouldOpen: shouldOpenProp,
    } = options;
    const context = instance.context;
    const doc = trigger.ownerDocument;
    let isHoverCloseActive = false;
    const cleanups = [];
    const on = (target, type, listener, opts) => {
      target.addEventListener(type, listener, opts);
      cleanups.push(() => target.removeEventListener(type, listener, opts));
    };

    const clickLike = () => isClickLikeOpenEvent(context.openEventType(), instance.interactedInside);
    const checkShouldOpen = () => shouldOpenProp?.() !== false;
    const isActiveTrigger = () => context.domReference() === trigger;

    function isOverInactiveTrigger(currentDomReference, currentTarget, target) {
      const allTriggers = context.triggers();
      if (allTriggers.includes(currentTarget)) {
        return !currentDomReference || !t().contains(currentDomReference, currentTarget);
      }
      if (!(target instanceof Element)) return false;
      return allTriggers.some((element) => t().contains(element, target)) &&
        (!currentDomReference || !t().contains(currentDomReference, target));
    }

    function cleanupMouseMoveHandler() {
      if (!instance.handler) return;
      doc.removeEventListener("mousemove", instance.handler);
      instance.handler = undefined;
    }

    const clearPointerEvents = () => clearSafePolygonPointerEventsMutation(instance);

    instance.handleCloseOptions = handleClose?.__options;

    // When closing before opening, clear the delay timeouts to cancel it
    // from showing.
    function onOpenChangeLocal(open, reason) {
      if (!open) {
        isHoverCloseActive = reason === "trigger-hover";
        cleanupMouseMoveHandler();
        instance.openChangeTimeout.clear();
        instance.restTimeout.clear();
        instance.blockMouseMove = true;
        instance.restTimeoutPending = false;
      } else {
        isHoverCloseActive = false;
      }
    }
    instance.onOpenChangeLocal.add(onOpenChangeLocal);
    cleanups.push(() => instance.onOpenChangeLocal.delete(onOpenChangeLocal));

    function setOpen(open, event) {
      context.onOpenChange(open, "trigger-hover", event);
    }

    function closeWithDelay(event, runElseBranch = true) {
      const closeDelay = getDelay(delay, "close", instance.pointerType);
      if (closeDelay) {
        instance.openChangeTimeout.start(closeDelay, () => {
          setOpen(false, event);
          emitClosed(event);
        });
      } else if (runElseBranch) {
        instance.openChangeTimeout.clear();
        setOpen(false, event);
        emitClosed(event);
      }
    }

    function onMouseEnter(event) {
      if (!enabled()) return;
      instance.openChangeTimeout.clear();
      instance.blockMouseMove = false;
      if (mouseOnly && !isMouseLikePointerType(instance.pointerType)) return;
      // Only a rest delay is set, with no fallback delay: onMouseMove opens.
      const restMsValue = getRestMs(restMs);
      const openDelay = getDelay(delay, "open", instance.pointerType);
      const currentDomReference = context.domReference();
      const triggerNode = trigger;
      const isOverInactive = isOverInactiveTrigger(currentDomReference, triggerNode, event.target);
      const isOpen = context.isOpen();
      const isInClosingTransition = isClosing?.() ?? false;
      const isHoverCloseTransition = !isOpen && isInClosingTransition && isHoverCloseActive;
      const isReenteringSameTriggerDuringCloseTransition = !isOverInactive && currentDomReference instanceof Element &&
        t().contains(currentDomReference, triggerNode) && isHoverCloseTransition;
      const isRestOnlyDelay = restMsValue > 0 && !openDelay;
      const shouldOpenImmediately = (isOverInactive && (isOpen || isHoverCloseTransition)) || isReenteringSameTriggerDuringCloseTransition;
      const shouldOpen = !isOpen || isOverInactive;
      // Moving between triggers while open, or back during a hover close,
      // opens at once.
      if (shouldOpenImmediately) {
        if (checkShouldOpen()) setOpen(true, event);
        return;
      }
      if (isRestOnlyDelay) return;
      if (openDelay) {
        instance.openChangeTimeout.start(openDelay, () => {
          if (shouldOpen && checkShouldOpen()) setOpen(true, event);
        });
      } else if (shouldOpen && checkShouldOpen()) {
        setOpen(true, event);
      }
    }

    function onMouseLeave(event) {
      if (!enabled()) return;
      if (clickLike()) {
        clearPointerEvents();
        return;
      }
      cleanupMouseMoveHandler();
      instance.restTimeout.clear();
      instance.restTimeoutPending = false;
      if (isInsideEnabledTrigger(event.relatedTarget, context.triggers())) return;
      if (handleClose) {
        if (!context.isOpen()) instance.openChangeTimeout.clear();
        const currentTrigger = trigger;
        instance.handler = handleClose({
          x: event.clientX,
          y: event.clientY,
          placement: context.placement(),
          elements: { domReference: context.domReference(), floating: context.floating() },
          hasOpenChild: () => openChildrenOf(context.floating()).length > 0,
          onClose() {
            clearPointerEvents();
            cleanupMouseMoveHandler();
            if (enabled() && !clickLike() && currentTrigger === context.domReference()) closeWithDelay(event, true);
          },
        });
        doc.addEventListener("mousemove", instance.handler);
        instance.handler(event);
        return;
      }
      const shouldClose = instance.pointerType === "touch" ? !t().contains(context.floating(), event.relatedTarget) : true;
      if (shouldClose) closeWithDelay(event);
    }

    // The trigger's props.
    function setPointerRef(event) {
      instance.pointerType = event.pointerType;
    }

    function onMouseMove(event) {
      if (!enabled()) return;
      const currentDomReference = context.domReference();
      const currentOpen = context.isOpen();
      const isOverInactive = isOverInactiveTrigger(currentDomReference, trigger, event.target);
      if (mouseOnly && !isMouseLikePointerType(instance.pointerType)) return;
      if (currentOpen && isOverInactive && instance.handleCloseOptions?.blockPointerEvents) {
        const floatingElement = context.floating();
        if (floatingElement) {
          applySafePolygonPointerEventsMutation(instance, {
            scopeElement: instance.handleCloseOptions?.getScope?.() ?? doc.body,
            referenceElement: trigger,
            floatingElement,
          });
        }
      }
      const restMsValue = getRestMs(restMs);
      if ((currentOpen && !isOverInactive) || restMsValue === 0) return;
      if (!isOverInactive && instance.restTimeoutPending && event.movementX ** 2 + event.movementY ** 2 < 2) return;
      instance.restTimeout.clear();
      function handleMouseMove() {
        instance.restTimeoutPending = false;
        // A delayed hover open does not override a click like open that
        // happened while the delay was pending.
        if (clickLike()) return;
        const latestOpen = context.isOpen();
        if (!instance.blockMouseMove && (!latestOpen || isOverInactive) && checkShouldOpen()) setOpen(true, event);
      }
      if (instance.pointerType === "touch" || (isOverInactive && currentOpen)) {
        handleMouseMove();
      } else {
        instance.restTimeoutPending = true;
        instance.restTimeout.start(restMsValue, handleMouseMove);
      }
    }

    on(trigger, "pointerdown", setPointerRef);
    on(trigger, "pointerenter", setPointerRef);
    on(trigger, "mousemove", onMouseMove);
    if (move) on(trigger, "mousemove", onMouseEnter, { once: true });
    on(trigger, "mouseenter", onMouseEnter);
    on(trigger, "mouseleave", onMouseLeave);
    return () => {
      cleanupMouseMoveHandler();
      cleanups.splice(0).forEach((cleanup) => cleanup());
      if (isActiveTrigger()) instance.handleCloseOptions = undefined;
    };
  }

  // ----- useHoverFloatingInteraction.ts ---------------------------------------

  function useHoverFloatingInteraction(instance, options = {}) {
    const { enabled = () => true, closeDelay = 0 } = options;
    const context = instance.context;
    const childClosedTimeout = new Timeout();
    const cleanups = [];
    let listening = null;

    const clickLike = () => isClickLikeOpenEvent(context.openEventType(), instance.interactedInside);
    const isHoverOpen = () => isHoverOpenEvent(context.openEventType());
    const clearPointerEvents = () => clearSafePolygonPointerEventsMutation(instance);

    // The source's layout effects on open.
    function onOpenChangeLocal(open) {
      if (!open) {
        instance.pointerType = undefined;
        instance.restTimeoutPending = false;
        instance.interactedInside = false;
        clearPointerEvents();
        return;
      }
      const reference = context.domReference();
      const floating = context.floating();
      if (!enabled() || !instance.handleCloseOptions?.blockPointerEvents || !isHoverOpen() || !(reference instanceof Element) || !floating) return;
      const parentFloating = context.parentFloating();
      if (parentFloating) parentFloating.style.pointerEvents = "";
      // A cached scope or parent that is the floating element itself would
      // not shield the sibling items in the parent menu.
      const cachedScopeElement = instance.pointerEventsScopeElement !== floating ? instance.pointerEventsScopeElement : null;
      const parentScopeElement = parentFloating !== floating ? parentFloating : null;
      const scopeElement = instance.handleCloseOptions?.getScope?.() ?? cachedScopeElement ?? parentScopeElement ??
        reference.closest("[data-rootownerid]") ?? floating.ownerDocument.body;
      applySafePolygonPointerEventsMutation(instance, { scopeElement, referenceElement: reference, floatingElement: floating });
    }
    instance.onOpenChangeLocal.add(onOpenChangeLocal);
    cleanups.push(() => instance.onOpenChangeLocal.delete(onOpenChangeLocal));

    function hasParentChildren() {
      return openChildrenOf(context.parentFloating()).length > 0;
    }

    function setClosed(event) {
      context.onOpenChange(false, "trigger-hover", event);
      emitClosed(event);
    }

    function closeWithDelay(event) {
      const delay = getDelay(closeDelay, "close", instance.pointerType);
      if (delay) {
        instance.openChangeTimeout.start(delay, () => setClosed(event));
      } else {
        instance.openChangeTimeout.clear();
        setClosed(event);
      }
    }

    function handleInteractInside(event) {
      if (!enabled()) return;
      const target = event.target;
      if (!isInteractiveElement(target)) {
        instance.interactedInside = false;
        return;
      }
      instance.interactedInside = target?.closest("[aria-haspopup]") != null;
    }

    function stopListening() {
      if (listening) closedListeners.delete(listening);
      listening = null;
    }

    // A child closed, maybe because the pointer moved into this parent: the
    // mouseenter gets a chance to fire first.
    function onNodeClosed(event) {
      if (!context.parentFloating() || hasParentChildren()) return;
      childClosedTimeout.start(0, () => {
        stopListening();
        setClosed(event);
      });
    }

    function onFloatingMouseEnter() {
      if (!enabled()) return;
      instance.openChangeTimeout.clear();
      childClosedTimeout.clear();
      stopListening();
      clearPointerEvents();
    }

    function onFloatingMouseLeave(event) {
      if (!enabled()) return;
      if (hasParentChildren()) {
        stopListening();
        listening = onNodeClosed;
        closedListeners.add(listening);
        return;
      }
      // Leaving toward another trigger moves the popup instead.
      if (isInsideEnabledTrigger(event.relatedTarget, context.triggers())) return;
      const relatedTarget = event.relatedTarget;
      const isMovingIntoDescendantFloating = relatedTarget instanceof Element &&
        openChildrenOf(context.floating()).some((child) => t().contains(child.floating(), relatedTarget));
      if (isMovingIntoDescendantFloating) return;
      // An active safePolygon handler decides the close.
      if (instance.handler) {
        instance.handler(event);
        return;
      }
      clearPointerEvents();
      if (isHoverOpen() && !clickLike()) closeWithDelay(event);
    }

    // The floating element mounts on open, so the listeners follow it.
    let floating = null;
    function attach() {
      const next = context.floating();
      if (next === floating) return;
      detach();
      floating = next;
      if (!floating) return;
      floating.addEventListener("mouseenter", onFloatingMouseEnter);
      floating.addEventListener("mouseleave", onFloatingMouseLeave);
      floating.addEventListener("pointerdown", handleInteractInside, true);
    }
    function detach() {
      if (!floating) return;
      floating.removeEventListener("mouseenter", onFloatingMouseEnter);
      floating.removeEventListener("mouseleave", onFloatingMouseLeave);
      floating.removeEventListener("pointerdown", handleInteractInside, true);
      floating = null;
    }
    const onOpenAttach = (open) => {
      if (open) attach();
    };
    instance.onOpenChangeLocal.add(onOpenAttach);
    cleanups.push(() => instance.onOpenChangeLocal.delete(onOpenAttach));
    attach();

    return () => {
      detach();
      stopListening();
      childClosedTimeout.clear();
      clearPointerEvents();
      cleanups.splice(0).forEach((cleanup) => cleanup());
    };
  }

  // ----- safePolygon.ts ------------------------------------------------------

  const CURSOR_SPEED_THRESHOLD = 0.1;
  const CURSOR_SPEED_THRESHOLD_SQUARED = CURSOR_SPEED_THRESHOLD * CURSOR_SPEED_THRESHOLD;
  const POLYGON_BUFFER = 0.5;

  function hasIntersectingEdge(pointX, pointY, xi, yi, xj, yj) {
    return yi >= pointY !== yj >= pointY && pointX <= ((xj - xi) * (pointY - yi)) / (yj - yi) + xi;
  }

  function isPointInQuadrilateral(pointX, pointY, x1, y1, x2, y2, x3, y3, x4, y4) {
    let isInsideValue = false;
    if (hasIntersectingEdge(pointX, pointY, x1, y1, x2, y2)) isInsideValue = !isInsideValue;
    if (hasIntersectingEdge(pointX, pointY, x2, y2, x3, y3)) isInsideValue = !isInsideValue;
    if (hasIntersectingEdge(pointX, pointY, x3, y3, x4, y4)) isInsideValue = !isInsideValue;
    if (hasIntersectingEdge(pointX, pointY, x4, y4, x1, y1)) isInsideValue = !isInsideValue;
    return isInsideValue;
  }

  function isInsideRect(pointX, pointY, rect) {
    return pointX >= rect.x && pointX <= rect.x + rect.width && pointY >= rect.y && pointY <= rect.y + rect.height;
  }

  function isInsideAxisAlignedRect(pointX, pointY, x1, y1, x2, y2) {
    return pointX >= Math.min(x1, x2) && pointX <= Math.max(x1, x2) && pointY >= Math.min(y1, y2) && pointY <= Math.max(y1, y2);
  }

  // A safe area the pointer can cross from the trigger to the popup without
  // closing it.
  function safePolygon(options = {}) {
    const { blockPointerEvents = false } = options;
    const timeout = new Timeout();
    const fn = ({ x, y, placement, elements, onClose, hasOpenChild }) => {
      const side = placement?.split("-")[0];
      let hasLanded = false;
      let lastX = null;
      let lastY = null;
      let lastCursorTime = performance.now();

      function isCursorMovingSlowly(nextX, nextY) {
        const currentTime = performance.now();
        const elapsedTime = currentTime - lastCursorTime;
        if (lastX === null || lastY === null || elapsedTime === 0) {
          lastX = nextX;
          lastY = nextY;
          lastCursorTime = currentTime;
          return false;
        }
        const deltaX = nextX - lastX;
        const deltaY = nextY - lastY;
        const distanceSquared = deltaX * deltaX + deltaY * deltaY;
        const thresholdSquared = elapsedTime * elapsedTime * CURSOR_SPEED_THRESHOLD_SQUARED;
        lastX = nextX;
        lastY = nextY;
        lastCursorTime = currentTime;
        return distanceSquared < thresholdSquared;
      }

      function close() {
        timeout.clear();
        onClose();
      }

      return function onMouseMove(event) {
        timeout.clear();
        const domReference = elements.domReference;
        const floating = elements.floating;
        if (!domReference || !floating || side == null || x == null || y == null) return;
        const { clientX, clientY } = event;
        const target = event.target;
        const isLeave = event.type === "mouseleave";
        const isOverFloatingEl = t().contains(floating, target);
        const isOverReferenceEl = t().contains(domReference, target);
        if (isOverFloatingEl) {
          hasLanded = true;
          if (!isLeave) return;
        }
        if (isOverReferenceEl) {
          hasLanded = false;
          if (!isLeave) {
            hasLanded = true;
            return;
          }
        }
        // An overlapping popup would get stuck in an open close loop
        // (floating-ui/floating-ui#1910).
        if (isLeave && event.relatedTarget instanceof Element && t().contains(floating, event.relatedTarget)) return;
        const closeIfNoOpenChild = () => {
          if (!hasOpenChild()) close();
        };
        // A nested child is open: abort.
        if (hasOpenChild()) return;
        const refRect = domReference.getBoundingClientRect();
        const rect = floating.getBoundingClientRect();
        const cursorLeaveFromRight = x > rect.right - rect.width / 2;
        const cursorLeaveFromBottom = y > rect.bottom - rect.height / 2;
        const isFloatingWider = rect.width > refRect.width;
        const isFloatingTaller = rect.height > refRect.height;
        const left = (isFloatingWider ? refRect : rect).left;
        const right = (isFloatingWider ? refRect : rect).right;
        const top = (isFloatingTaller ? refRect : rect).top;
        const bottom = (isFloatingTaller ? refRect : rect).bottom;
        // Leaving from the opposite side closes. 1 absorbs rounding errors.
        if ((side === "top" && y >= refRect.bottom - 1) || (side === "bottom" && y <= refRect.top + 1) ||
          (side === "left" && x >= refRect.right - 1) || (side === "right" && x <= refRect.left + 1)) {
          closeIfNoOpenChild();
          return;
        }
        // The rectangular trough between the two elements always stays open.
        let isInsideTroughRect = false;
        switch (side) {
          case "top":
            isInsideTroughRect = isInsideAxisAlignedRect(clientX, clientY, left, refRect.top + 1, right, rect.bottom - 1);
            break;
          case "bottom":
            isInsideTroughRect = isInsideAxisAlignedRect(clientX, clientY, left, rect.top + 1, right, refRect.bottom - 1);
            break;
          case "left":
            isInsideTroughRect = isInsideAxisAlignedRect(clientX, clientY, rect.right - 1, bottom, refRect.left + 1, top);
            break;
          case "right":
            isInsideTroughRect = isInsideAxisAlignedRect(clientX, clientY, refRect.right - 1, bottom, rect.left + 1, top);
            break;
          default:
        }
        if (isInsideTroughRect) return;
        if (hasLanded && !isInsideRect(clientX, clientY, refRect)) {
          closeIfNoOpenChild();
          return;
        }
        if (!isLeave && isCursorMovingSlowly(clientX, clientY)) {
          closeIfNoOpenChild();
          return;
        }
        let isInsidePolygon = false;
        switch (side) {
          case "top": {
            const cursorXOffset = isFloatingWider ? POLYGON_BUFFER / 2 : POLYGON_BUFFER * 4;
            const cursorPointOneX = isFloatingWider ? x + cursorXOffset : cursorLeaveFromRight ? x + cursorXOffset : x - cursorXOffset;
            const cursorPointTwoX = isFloatingWider ? x - cursorXOffset : cursorLeaveFromRight ? x + cursorXOffset : x - cursorXOffset;
            const cursorPointY = y + POLYGON_BUFFER + 1;
            const commonYLeft = cursorLeaveFromRight ? rect.bottom - POLYGON_BUFFER : isFloatingWider ? rect.bottom - POLYGON_BUFFER : rect.top;
            const commonYRight = cursorLeaveFromRight ? (isFloatingWider ? rect.bottom - POLYGON_BUFFER : rect.top) : rect.bottom - POLYGON_BUFFER;
            isInsidePolygon = isPointInQuadrilateral(clientX, clientY, cursorPointOneX, cursorPointY, cursorPointTwoX, cursorPointY, rect.left, commonYLeft, rect.right, commonYRight);
            break;
          }
          case "bottom": {
            const cursorXOffset = isFloatingWider ? POLYGON_BUFFER / 2 : POLYGON_BUFFER * 4;
            const cursorPointOneX = isFloatingWider ? x + cursorXOffset : cursorLeaveFromRight ? x + cursorXOffset : x - cursorXOffset;
            const cursorPointTwoX = isFloatingWider ? x - cursorXOffset : cursorLeaveFromRight ? x + cursorXOffset : x - cursorXOffset;
            const cursorPointY = y - POLYGON_BUFFER;
            const commonYLeft = cursorLeaveFromRight ? rect.top + POLYGON_BUFFER : isFloatingWider ? rect.top + POLYGON_BUFFER : rect.bottom;
            const commonYRight = cursorLeaveFromRight ? (isFloatingWider ? rect.top + POLYGON_BUFFER : rect.bottom) : rect.top + POLYGON_BUFFER;
            isInsidePolygon = isPointInQuadrilateral(clientX, clientY, cursorPointOneX, cursorPointY, cursorPointTwoX, cursorPointY, rect.left, commonYLeft, rect.right, commonYRight);
            break;
          }
          case "left": {
            const cursorYOffset = isFloatingTaller ? POLYGON_BUFFER / 2 : POLYGON_BUFFER * 4;
            const cursorPointOneY = isFloatingTaller ? y + cursorYOffset : cursorLeaveFromBottom ? y + cursorYOffset : y - cursorYOffset;
            const cursorPointTwoY = isFloatingTaller ? y - cursorYOffset : cursorLeaveFromBottom ? y + cursorYOffset : y - cursorYOffset;
            const cursorPointX = x + POLYGON_BUFFER + 1;
            const commonXTop = cursorLeaveFromBottom ? rect.right - POLYGON_BUFFER : isFloatingTaller ? rect.right - POLYGON_BUFFER : rect.left;
            const commonXBottom = cursorLeaveFromBottom ? (isFloatingTaller ? rect.right - POLYGON_BUFFER : rect.left) : rect.right - POLYGON_BUFFER;
            isInsidePolygon = isPointInQuadrilateral(clientX, clientY, commonXTop, rect.top, commonXBottom, rect.bottom, cursorPointX, cursorPointOneY, cursorPointX, cursorPointTwoY);
            break;
          }
          case "right": {
            const cursorYOffset = isFloatingTaller ? POLYGON_BUFFER / 2 : POLYGON_BUFFER * 4;
            const cursorPointOneY = isFloatingTaller ? y + cursorYOffset : cursorLeaveFromBottom ? y + cursorYOffset : y - cursorYOffset;
            const cursorPointTwoY = isFloatingTaller ? y - cursorYOffset : cursorLeaveFromBottom ? y + cursorYOffset : y - cursorYOffset;
            const cursorPointX = x - POLYGON_BUFFER;
            const commonXTop = cursorLeaveFromBottom ? rect.left + POLYGON_BUFFER : isFloatingTaller ? rect.left + POLYGON_BUFFER : rect.right;
            const commonXBottom = cursorLeaveFromBottom ? (isFloatingTaller ? rect.left + POLYGON_BUFFER : rect.right) : rect.left + POLYGON_BUFFER;
            isInsidePolygon = isPointInQuadrilateral(clientX, clientY, cursorPointX, cursorPointOneY, cursorPointX, cursorPointTwoY, commonXTop, rect.top, commonXBottom, rect.bottom);
            break;
          }
          default:
        }
        if (!isInsidePolygon) closeIfNoOpenChild();
        else if (!hasLanded) timeout.start(40, closeIfNoOpenChild);
      };
    };
    fn.__options = { ...options, blockPointerEvents };
    return fn;
  }

  window.templ = window.templ || {};
  window.templ.hover = {
    createHoverInteraction,
    useHoverReferenceInteraction,
    useHoverFloatingInteraction,
    safePolygon,
    applySafePolygonPointerEventsMutation,
    clearSafePolygonPointerEventsMutation,
  };
})();

// components/baseui/use_list_navigation.js
// Port of @base-ui/react floating-ui-react/hooks/useListNavigation.ts (1.6.0)
// with the list helpers of floating-ui-react/utils/composite.ts. Arrow key
// navigation of a list, with real focus or, for a combobox, virtual focus.
//
//   const nav = window.templ.listNavigation.useListNavigation(options)   when the component mounts
//   nav.open()     after the popup opened, the source's open effects
//   nav.close()    when it closes
//   nav.sync()     after the component changed its active index itself
//   nav.cleanup()  when the component unmounts
//
//   floating            the popup, the element that takes focus
//   reference           the trigger, or the input of a combobox
//   items()             the list, in order (listRef)
//   activeIndex()       the component's active index, or null
//   selectedIndex()     the selected index, or null
//   onNavigate(index, event)   sets the component's active index (null for none)
//   onOpenChange(open, reason, event)  opens on an arrow key, closes a nested list
//   isOpen()
//   loopFocus, nested, rtl, virtual, orientation, parentOrientation, allowEscape,
//   focusItemOnOpen ("auto", true, false), focusItemOnHover, openOnArrowKeyDown,
//   resetOnPointerLeave, disabledIndices (an array, a function, or undefined)
//   onVirtualFocus(item)  for virtual lists, the source's virtualfocus event
//
// Left out: grid navigation, which only a grid combobox uses.
(function () {
  "use strict";

  const t = () => window.templ.tabbable;
  const ARROW_UP = "ArrowUp";
  const ARROW_DOWN = "ArrowDown";
  const ARROW_LEFT = "ArrowLeft";
  const ARROW_RIGHT = "ArrowRight";

  function doSwitch(orientation, vertical, horizontal) {
    switch (orientation) {
      case "vertical": return vertical;
      case "horizontal": return horizontal;
      default: return vertical || horizontal;
    }
  }

  function isMainOrientationKey(key, orientation) {
    const vertical = key === ARROW_UP || key === ARROW_DOWN;
    const horizontal = key === ARROW_LEFT || key === ARROW_RIGHT;
    return doSwitch(orientation, vertical, horizontal);
  }

  function isMainOrientationToEndKey(key, orientation, rtl) {
    const vertical = key === ARROW_DOWN;
    const horizontal = rtl ? key === ARROW_LEFT : key === ARROW_RIGHT;
    return doSwitch(orientation, vertical, horizontal) || key === "Enter" || key === " " || key === "";
  }

  function isCrossOrientationOpenKey(key, orientation, rtl) {
    const vertical = rtl ? key === ARROW_LEFT : key === ARROW_RIGHT;
    const horizontal = key === ARROW_DOWN;
    return doSwitch(orientation, vertical, horizontal);
  }

  function isCrossOrientationCloseKey(key, orientation, rtl) {
    const vertical = rtl ? key === ARROW_RIGHT : key === ARROW_LEFT;
    const horizontal = key === ARROW_UP;
    if (orientation === "both") return key === "Escape";
    return doSwitch(orientation, vertical, horizontal);
  }

  // utils/composite.ts, see composite.js.
  const c = () => window.templ.composite;
  const isIndexOutOfListBounds = (...args) => c().isIndexOutOfListBounds(...args);
  const isListIndexDisabled = (...args) => c().isListIndexDisabled(...args);
  const findNonDisabledListIndex = (...args) => c().findNonDisabledListIndex(...args);
  const getMinListIndex = (...args) => c().getMinListIndex(...args);
  const getMaxListIndex = (...args) => c().getMaxListIndex(...args);

  const isTypeableCombobox = (element) => !!element && element.getAttribute("role") === "combobox" &&
    element.matches("input:not([type='hidden']):not([disabled]),[contenteditable]:not([contenteditable='false']),textarea:not([disabled])");
  const stopEvent = (event) => {
    event.preventDefault();
    event.stopPropagation();
  };

  function isVirtualClick(event) {
    if (event.pointerType === "" && event.isTrusted) return true;
    return event.detail === 0 && !event.pointerType;
  }

  function isVirtualPointerEvent(event) {
    return (event.width === 0 && event.height === 0) ||
      (event.width < 1 && event.height < 1 && event.pressure === 0 && event.detail === 0 && event.pointerType === "touch");
  }

  // ----- useListNavigation -------------------------------------------------------

  function useListNavigation(options) {
    const {
      floating,
      reference = null,
      items,
      activeIndex = () => null,
      selectedIndex = () => null,
      onNavigate: onNavigateProp = () => {},
      onOpenChange = () => {},
      isOpen,
      allowEscape = false,
      loopFocus = false,
      nested = false,
      rtl = false,
      virtual = false,
      focusItemOnOpen = "auto",
      focusItemOnHover = true,
      openOnArrowKeyDown = true,
      disabledIndices,
      orientation = "vertical",
      parentOrientation,
      resetOnPointerLeave = true,
      onVirtualFocus,
    } = options;
    const typeableComboboxReference = isTypeableCombobox(reference);
    let focusItemOnOpenState = focusItemOnOpen;
    let indexRef = selectedIndex() ?? -1;
    let key = null;
    let isPointerModality = true;
    let forceSyncFocus = false;
    let forceScrollIntoView = false;
    let focusFrame = 0;
    let cancelQueuedFocus = null;
    const cleanups = [];

    const list = () => items();
    const onNavigate = (event) => {
      onNavigateProp(indexRef === -1 ? null : indexRef, event);
      sync();
    };

    function focusItem() {
      const runFocus = (item) => {
        if (virtual) onVirtualFocus?.(item);
        else cancelQueuedFocus = window.templ.focusManager.enqueueFocus(item, { sync: forceSyncFocus, preventScroll: true });
      };
      const initialItem = list()[indexRef];
      const scrollIntoView = forceScrollIntoView;
      if (initialItem) runFocus(initialItem);
      const run = () => {
        const waitedItem = list()[indexRef] || initialItem;
        if (!waitedItem) return;
        if (!initialItem) runFocus(waitedItem);
        if (scrollIntoView || !isPointerModality) waitedItem.scrollIntoView?.({ block: "nearest", inline: "nearest" });
      };
      if (forceSyncFocus) {
        run();
      } else {
        cancelAnimationFrame(focusFrame);
        focusFrame = requestAnimationFrame(run);
      }
    }

    // The source's effect on activeIndex: focus the active item while open.
    function sync() {
      if (!isOpen()) {
        forceSyncFocus = false;
        return;
      }
      const active = activeIndex();
      if (active == null) {
        forceSyncFocus = false;
        return;
      }
      if (!isIndexOutOfListBounds(list(), active)) {
        indexRef = active;
        focusItem();
        forceScrollIntoView = false;
      }
    }

    // The source's open effects: start at the selected item, or focus the
    // first or last item when a key or a virtual click opened the list.
    function open() {
      indexRef = selectedIndex() ?? -1;
      if (focusItemOnOpenState && selectedIndex() != null) {
        // The selected item comes into view regardless of the modality.
        forceScrollIntoView = true;
        onNavigate();
        return;
      }
      if (activeIndex() != null) {
        sync();
        return;
      }
      if (focusItemOnOpenState && (key != null || (focusItemOnOpenState === true && key == null))) {
        let runs = 0;
        const waitForListPopulated = () => {
          if (list()[0] == null) {
            if (runs < 2) (runs ? requestAnimationFrame : queueMicrotask)(waitForListPopulated);
            runs += 1;
            return;
          }
          // The first non disabled item, skipping attribute disabled ones
          // even with an empty disabledIndices (mui/base-ui#2604).
          indexRef = key == null || isMainOrientationToEndKey(key, orientation, rtl) || nested
            ? getMinListIndex(list())
            : getMaxListIndex(list());
          key = null;
          onNavigate();
        };
        waitForListPopulated();
      }
    }

    function close() {
      indexRef = -1;
      onNavigateProp(null);
      forceSyncFocus = false;
      key = null;
      focusItemOnOpenState = focusItemOnOpen;
    }

    const on = (target, type, listener, capture) => {
      if (!target) return;
      target.addEventListener(type, listener, !!capture);
      cleanups.push(() => target.removeEventListener(type, listener, !!capture));
    };

    function syncCurrentTarget(event, item) {
      if (!isOpen()) return;
      const index = list().indexOf(item);
      if (index !== -1 && (indexRef !== index || activeIndex() !== index)) {
        indexRef = index;
        onNavigate(event);
      }
    }

    function commonOnKeyDown(event) {
      isPointerModality = false;
      forceSyncFocus = true;
      // Chrome fires ArrowDown twice while composing.
      if (event.which === 229) return;
      // Animating out: ignore navigation.
      if (!isOpen() && event.currentTarget === floating) return;

      if (nested && isCrossOrientationCloseKey(event.key, orientation, rtl)) {
        // The parent navigates with this key too: let it.
        if (!isMainOrientationKey(event.key, parentOrientation)) stopEvent(event);
        onOpenChange(false, "list-navigation", event);
        if (reference instanceof HTMLElement) {
          if (virtual) onVirtualFocus?.(reference);
          else reference.focus();
        }
        return;
      }
      const currentIndex = indexRef;
      const items = list();
      const minIndex = getMinListIndex(items, disabledIndices);
      const maxIndex = getMaxListIndex(items, disabledIndices);
      if (!typeableComboboxReference) {
        if (event.key === "Home") {
          stopEvent(event);
          indexRef = minIndex;
          onNavigate(event);
        }
        if (event.key === "End") {
          stopEvent(event);
          indexRef = maxIndex;
          onNavigate(event);
        }
      }
      if (!isMainOrientationKey(event.key, orientation)) return;
      stopEvent(event);
      // No item focused: start at the end the key points to.
      if (isOpen() && !virtual && t().activeElement(event.currentTarget.ownerDocument) === event.currentTarget) {
        indexRef = isMainOrientationToEndKey(event.key, orientation, rtl) ? minIndex : maxIndex;
        onNavigate(event);
        return;
      }
      if (isMainOrientationToEndKey(event.key, orientation, rtl)) {
        if (loopFocus) {
          if (currentIndex >= maxIndex) {
            if (allowEscape && currentIndex !== items.length) {
              indexRef = -1;
            } else {
              forceSyncFocus = false;
              indexRef = minIndex;
            }
          } else {
            indexRef = findNonDisabledListIndex(items, { startingIndex: currentIndex, disabledIndices });
          }
        } else {
          indexRef = Math.min(maxIndex, findNonDisabledListIndex(items, { startingIndex: currentIndex, disabledIndices }));
        }
      } else if (loopFocus) {
        if (currentIndex <= minIndex) {
          if (allowEscape && currentIndex !== -1) {
            indexRef = items.length;
          } else {
            forceSyncFocus = false;
            indexRef = maxIndex;
          }
        } else {
          indexRef = findNonDisabledListIndex(items, { startingIndex: currentIndex, decrement: true, disabledIndices });
        }
      } else {
        indexRef = Math.max(minIndex, findNonDisabledListIndex(items, { startingIndex: currentIndex, decrement: true, disabledIndices }));
      }
      if (isIndexOutOfListBounds(items, indexRef)) indexRef = -1;
      onNavigate(event);
    }

    // The floating element's props.
    on(floating, "keydown", (event) => {
      // Shift+Tab closes a submenu. An element nested in the popup with its
      // own focus management, like a dialog opened from the menu, keeps it.
      if (event.key === "Tab" && event.shiftKey && isOpen() && !virtual) {
        if (!t().contains(floating, event.composedPath?.()[0] || event.target)) return;
        stopEvent(event);
        onOpenChange(false, "focus-out", event);
        if (reference instanceof HTMLElement) reference.focus();
        return;
      }
      commonOnKeyDown(event);
    });
    on(floating, "pointermove", () => {
      isPointerModality = true;
    });
    if (orientation !== "both") floating?.setAttribute("aria-orientation", orientation);

    // The items' props, delegated since the list changes.
    const itemOf = (target) => {
      const items = list();
      for (let node = target; node && node !== floating; node = node.parentElement) {
        if (items.includes(node)) return node;
      }
      return null;
    };
    on(floating, "focusin", (event) => {
      const item = itemOf(event.target);
      if (!item) return;
      forceSyncFocus = true;
      syncCurrentTarget(event, item);
    });
    on(floating, "click", (event) => {
      itemOf(event.target)?.focus({ preventScroll: true });
    });
    on(floating, "mousemove", (event) => {
      const item = itemOf(event.target);
      if (!item) return;
      forceSyncFocus = true;
      forceScrollIntoView = false;
      if (focusItemOnHover) syncCurrentTarget(event, item);
    });
    on(floating, "pointerout", (event) => {
      const item = itemOf(event.target);
      if (!item || item.contains(event.relatedTarget)) return;
      if (!isOpen() || !isPointerModality || event.pointerType === "touch") return;
      forceSyncFocus = true;
      if (!focusItemOnHover || list().includes(event.relatedTarget) || !resetOnPointerLeave) return;
      cancelQueuedFocus?.();
      cancelQueuedFocus = null;
      indexRef = -1;
      onNavigate(event);
      if (!virtual && t().contains(floating, t().activeElement(floating.ownerDocument))) {
        floating.focus({ preventScroll: true });
      }
    });

    // The trigger's props.
    function openOnNavigationKeyDown(event) {
      onOpenChange(true, "list-navigation", event);
    }
    on(reference, "keydown", (event) => {
      const currentOpen = isOpen();
      isPointerModality = false;
      const isArrowKey = event.key.startsWith("Arrow");
      const isParentCrossOpenKey = isCrossOrientationOpenKey(event.key, parentOrientation, rtl);
      const isMainKey = isMainOrientationKey(event.key, orientation);
      const isNavigationKey = (nested ? isParentCrossOpenKey : isMainKey) || event.key === "Enter" || event.key.trim() === "";
      if (virtual && currentOpen) {
        commonOnKeyDown(event);
        return;
      }
      if (!currentOpen && !openOnArrowKeyDown && isArrowKey) return;
      if (isNavigationKey) {
        const isParentMainKey = isMainOrientationKey(event.key, parentOrientation);
        key = nested && isParentMainKey ? null : event.key;
      }
      if (nested) {
        if (isParentCrossOpenKey) {
          stopEvent(event);
          if (currentOpen) {
            indexRef = getMinListIndex(list(), disabledIndices);
            onNavigate(event);
          } else {
            openOnNavigationKeyDown(event);
          }
        }
        return;
      }
      if (isMainKey) {
        if (selectedIndex() != null) indexRef = selectedIndex();
        stopEvent(event);
        if (!currentOpen && openOnArrowKeyDown) openOnNavigationKeyDown(event);
        else commonOnKeyDown(event);
        if (currentOpen) onNavigate(event);
      }
    });
    on(reference, "focus", (event) => {
      if (isOpen() && !virtual) {
        indexRef = -1;
        onNavigate(event);
      }
    });
    const checkVirtualPointer = (event) => {
      // pointerdown fires first: reset, then check.
      focusItemOnOpenState = focusItemOnOpen;
      if (focusItemOnOpen === "auto" && isVirtualPointerEvent(event)) focusItemOnOpenState = true;
    };
    const checkVirtualMouse = (event) => {
      if (focusItemOnOpen === "auto" && isVirtualClick(event)) focusItemOnOpenState = !virtual;
    };
    on(reference, "pointerdown", checkVirtualPointer);
    on(reference, "pointerenter", checkVirtualPointer);
    on(reference, "mousedown", checkVirtualMouse);
    on(reference, "click", checkVirtualMouse);

    return {
      open,
      close,
      sync,
      cleanup() {
        cancelAnimationFrame(focusFrame);
        cleanups.splice(0).forEach((cleanup) => cleanup());
      },
    };
  }

  window.templ = window.templ || {};
  window.templ.listNavigation = { useListNavigation };
})();

// components/baseui/use_transition_status.js
// Port of @base-ui/react internals/useTransitionStatus.ts with
// useOpenChangeComplete.ts and useAnimationsFinished.ts (1.6.0).
//
// The popup and its backdrop render the status: data-open or data-closed,
// plus data-starting-style for the first frame of an open and
// data-ending-style while it closes. The positioner and the arrow render only
// data-open or data-closed (popupStateMapping), and the positioner has its
// transitions off while starting (getDisabledMountTransitionStyles) and no
// pointer events while closed (usePositioner's inert). The close
// completes once every animation on the animated element finished, which is
// when Base UI unmounts. Opening again before that cancels the pending close.
//
// status is an array of the parts that render the status, or
// { parts, positioner, stateParts, styleParts }. parts[0] carries the pending
// status; styleParts render only the starting and ending style (the
// collapsible trigger's mapping).
(function () {
  "use strict";

  function set(parts, name, present) {
    parts.forEach((part) => part?.toggleAttribute(name, present));
  }

  function resolve(status) {
    const { parts, positioner = null, stateParts = [], styleParts = [] } = Array.isArray(status) ? { parts: status } : status;
    const own = parts.filter(Boolean);
    return {
      parts: [...own, ...styleParts.filter(Boolean)],
      open: [...own, positioner, ...stateParts].filter(Boolean),
      positioner,
    };
  }

  // usePositioner's inert: a closed positioner takes no pointer events, also
  // during its exit animation. Opening only takes back that none, so a value
  // safePolygon set stays, as React leaves a style it did not render.
  function setOpen(status, isOpen) {
    set(status.open, "data-open", isOpen);
    set(status.open, "data-closed", !isOpen);
    const style = status.positioner?.style;
    if (!style) return;
    if (!isOpen) style.pointerEvents = "none";
    else if (style.pointerEvents === "none") style.pointerEvents = "";
  }

  // useAnimationsFinished: runs fn once every animation on the element
  // finished, as long as isCurrent() still holds. An animation aborted
  // because a property it depends on changed may be followed by a new one,
  // so check again before running fn.
  function whenAnimationsFinish(animated, isCurrent, fn) {
    const exec = () => {
      if (!isCurrent()) return;
      if (typeof animated?.getAnimations !== "function") return fn();
      Promise.all(animated.getAnimations().map((animation) => animation.finished)).then(() => {
        if (isCurrent()) fn();
      }, () => {
        const running = animated.getAnimations().some((a) => a.pending || a.playState !== "finished");
        if (running) exec();
        else if (isCurrent()) fn();
      });
    };
    // One frame, so the new style's animations are registered.
    requestAnimationFrame(exec);
  }

  // onComplete, when given, runs once the open animations on animated
  // finished (useOpenChangeComplete with open true).
  function open(statusParam, animated, onComplete) {
    const status = resolve(statusParam);
    const { parts, positioner } = status;
    const main = parts[0];
    const token = {};
    main._templTransition = token;
    set(parts, "data-ending-style", false);
    setOpen(status, true);
    set(parts, "data-starting-style", true);
    if (positioner) positioner.style.transition = "none";
    // Compute the starting style once, so transitions start from it.
    void main.offsetWidth;
    requestAnimationFrame(() => {
      if (main._templTransition !== token) return;
      set(parts, "data-starting-style", false);
      if (positioner) positioner.style.transition = "";
      if (onComplete) whenAnimationsFinish(animated, () => main._templTransition === token, onComplete);
    });
  }

  // deferEnding sets data-ending-style one frame after data-closed, Base UI's
  // deferEndingState, and calls onEnding then. onEnding returning false
  // completes the close at once.
  function close(statusParam, animated, onComplete, { deferEnding = false, onEnding } = {}) {
    const status = resolve(statusParam);
    const { parts, positioner } = status;
    const main = parts[0];
    const token = {};
    main._templTransition = token;
    set(parts, "data-starting-style", false);
    if (positioner) positioner.style.transition = "";
    setOpen(status, false);

    const done = () => {
      if (main._templTransition !== token) return;
      main._templTransition = null;
      set(parts, "data-ending-style", false);
      onComplete?.();
    };
    const ending = () => {
      set(parts, "data-ending-style", true);
      if (onEnding?.() === false) return done();
      whenAnimationsFinish(animated, () => main._templTransition === token, done);
    };
    if (!deferEnding) return ending();
    requestAnimationFrame(() => {
      if (main._templTransition === token) ending();
    });
  }

  // Sets the state without a transition, like an unmount without exit
  // animation, and cancels one in flight.
  function reset(statusParam, isOpen) {
    const status = resolve(statusParam);
    status.parts[0]._templTransition = null;
    set(status.parts, "data-starting-style", false);
    set(status.parts, "data-ending-style", false);
    if (status.positioner) status.positioner.style.transition = "";
    setOpen(status, isOpen);
  }

  // Whether a close is in flight, Base UI's transitionStatus === "ending".
  function isEnding(element) {
    return !!element?.hasAttribute("data-ending-style");
  }

  // useAnimationsFinished on its own, for parts whose status another store
  // drives, like the toast manager.
  function animationsFinished(element, fn) {
    whenAnimationsFinish(element, () => element.isConnected, fn);
  }

  window.templ = window.templ || {};
  window.templ.transition = { open, close, reset, isEnding, animationsFinished };
})();

// components/baseui/use_trigger_focus_guards.js
// Port of @base-ui/react utils/popups/useTriggerFocusGuards.ts (1.6.0) with the
// guards PopoverTrigger and MenuTrigger render around their trigger while
// their non modal popup is mounted.
//
//   const guards = window.templ.triggerFocusGuards.attach(trigger, options)
//   guards.focusTarget   the guard after the trigger, the focus manager's
//                        nextFocusableElement
//   guards.remove()      once the popup unmounted
//
//   positioner                 the popup's positioner
//   beforeContentFocusGuard()  the focus manager's guard before the content
//   onClose(event)             closes the popup (reason focus-out)
//
// Tabbing out of the trigger backwards closes the popup and moves on; tabbing
// forward enters the popup through its content guard, and leaving the popup
// forward closes it and moves past it.
(function () {
  "use strict";

  const t = () => window.templ.tabbable;

  function attach(trigger, { positioner, beforeContentFocusGuard, onClose }) {
    const { createFocusGuard } = window.templ.focusManager;
    const preFocusGuard = createFocusGuard(null, (event) => {
      onClose(event);
      t().getTabbableBeforeElement(preFocusGuard)?.focus();
    });
    const focusTarget = createFocusGuard(null, (event) => {
      if (positioner && t().isOutsideEvent(event, positioner)) {
        beforeContentFocusGuard()?.focus();
        return;
      }
      onClose(event);
      let nextTabbable = t().getTabbableAfterElement(focusTarget || trigger);
      while (nextTabbable !== null && t().contains(positioner, nextTabbable)) {
        const prevTabbable = nextTabbable;
        nextTabbable = t().getNextTabbable(nextTabbable);
        if (nextTabbable === prevTabbable) break;
      }
      nextTabbable?.focus();
    });
    trigger.before(preFocusGuard);
    trigger.after(focusTarget);
    return {
      focusTarget,
      remove() {
        preFocusGuard.remove();
        focusTarget.remove();
      },
    };
  }

  window.templ = window.templ || {};
  window.templ.triggerFocusGuards = { attach };
})();

// components/baseui/use_typeahead.js
// Port of @base-ui/react floating-ui-react/hooks/useTypeahead.ts (1.6.0).
// Typing a character moves to the next item whose label starts with what was
// typed, until the typing pauses for resetMs.
//
//   const typeahead = window.templ.typeahead.useTypeahead(options)
//   typeahead.reset()     when the popup opens or closes, the source's effect
//   typeahead.cleanup()
//
//   elements        the reference and the floating element, which listen
//   labels()        the items' labels, in list order (listRef)
//   items()         the items, for the visibility check (elementsRef)
//   activeIndex()   selectedIndex()   isOpen()
//   onMatch(index)  onTyping(typing)
//   disabledIndices an array, a function, or undefined
//   resetMs         default 750, Base UI's menus and selects pass 500
(function () {
  "use strict";

  const t = () => window.templ.tabbable;

  function useTypeahead(options) {
    const {
      elements,
      labels,
      items = () => [],
      activeIndex = () => null,
      selectedIndex = () => null,
      isOpen,
      onMatch,
      onTyping,
      disabledIndices,
      resetMs = 750,
    } = options;
    let string = "";
    let prevIndex = selectedIndex() ?? activeIndex() ?? -1;
    let matchIndex = null;
    let timer = 0;
    const cleanups = [];
    const stopEvent = (event) => {
      event.preventDefault();
      event.stopPropagation();
    };

    function isItemAvailable(index) {
      const element = items()[index];
      if (element && !t().isElementVisible(element)) return false;
      return disabledIndices == null || !window.templ.composite.isListIndexDisabled([], index, disabledIndices);
    }

    function getMatchingIndex(list, value, startIndex = 0) {
      if (list.length === 0) return -1;
      const normalizedStartIndex = ((startIndex % list.length) + list.length) % list.length;
      const lowerString = value.toLowerCase();
      for (let offset = 0; offset < list.length; offset += 1) {
        const index = (normalizedStartIndex + offset) % list.length;
        const text = list[index];
        if (!text?.toLowerCase().startsWith(lowerString) || !isItemAvailable(index)) continue;
        return index;
      }
      return -1;
    }

    function onKeyDown(event) {
      const listContent = labels();
      // Space continues a typeahead session in progress.
      if (string.length > 0 && event.key === " ") {
        stopEvent(event);
        onTyping?.(true);
      }
      if (string.length > 0 && string[0] !== " ") {
        if (getMatchingIndex(listContent, string) === -1 && event.key !== " ") onTyping?.(false);
      }
      if (listContent == null || event.key.length !== 1 || event.ctrlKey || event.metaKey || event.altKey) return;
      if (isOpen() && event.key !== " ") {
        stopEvent(event);
        onTyping?.(true);
      }
      const isNewSession = string === "";
      if (isNewSession) prevIndex = selectedIndex() ?? activeIndex() ?? -1;
      // Rapid presses of one letter cycle through the items starting with it,
      // unless a label repeats its first letter, like "llama".
      const allowRapidSuccessionOfFirstLetter = listContent.every((text, index) =>
        text && isItemAvailable(index) ? text[0]?.toLowerCase() !== text[1]?.toLowerCase() : true);
      if (allowRapidSuccessionOfFirstLetter && string === event.key) {
        string = "";
        prevIndex = matchIndex;
      }
      string += event.key;
      clearTimeout(timer);
      timer = setTimeout(() => {
        string = "";
        prevIndex = matchIndex;
        onTyping?.(false);
      }, resetMs);
      const start = (isNewSession ? selectedIndex() ?? activeIndex() ?? -1 : prevIndex) ?? 0;
      const index = getMatchingIndex(listContent, string, start + 1);
      if (index !== -1) {
        onMatch?.(index);
        matchIndex = index;
      } else if (event.key !== " ") {
        string = "";
        onTyping?.(false);
      }
    }

    // Leaving the reference and the popup ends the session.
    function onBlur(event) {
      const next = event.relatedTarget;
      if (elements.some((element) => t().contains(element, next))) return;
      clearTimeout(timer);
      string = "";
      prevIndex = matchIndex;
      onTyping?.(false);
    }

    elements.filter(Boolean).forEach((element) => {
      element.addEventListener("keydown", onKeyDown);
      element.addEventListener("focusout", onBlur);
      cleanups.push(() => {
        element.removeEventListener("keydown", onKeyDown);
        element.removeEventListener("focusout", onBlur);
      });
    });

    return {
      // The source's effects on open and selectedIndex.
      reset() {
        clearTimeout(timer);
        matchIndex = null;
        string = "";
        prevIndex = selectedIndex() ?? activeIndex() ?? -1;
      },
      cleanup() {
        clearTimeout(timer);
        cleanups.splice(0).forEach((cleanup) => cleanup());
      },
    };
  }

  window.templ = window.templ || {};
  window.templ.typeahead = { useTypeahead };
})();

// components/dialog/dialog.js
(function () {
  "use strict";

  // Vanilla port of Base UI's Dialog (packages/react/src/dialog): the portal
  // node, backdrop and popup are SSRd divs. The script wires the blocks in
  // components/baseui (portal, transition status, dismiss, focus manager,
  // scroll lock) the way DialogRoot and DialogPopup use them. "Unmount" is
  // the portal node getting [hidden] again.

  // ----- registry ------------------------------------------------------------

  // Popup element -> per-dialog state. The open stack orders open dialogs by
  // open time (last = topmost), like Base UI's nested dialog counts.
  const dialogs = new Map();
  const openStack = [];

  // A dialog popup (Dialog, Sheet, AlertDialog) is the dialog or alertdialog
  // element carrying Base UI's modal prop; the popover popup has role dialog
  // too but no modal prop. Its parent is the portal node (data-base-ui-portal)
  // that holds the backdrop and the popup.
  const POPUP = '[role="dialog"][data-templ-modal], [role="alertdialog"][data-templ-modal]';
  // Base UI's Dialog.Backdrop renders role="presentation".
  const BACKDROP = ':scope > [role="presentation"]';

  function getDialog(target) {
    if (!target) return null;
    if (typeof target === "string") {
      const el = document.getElementById(target);
      return el && el.matches(POPUP) ? el : null;
    }
    if (target.matches?.(POPUP)) return target;
    return target.closest?.(POPUP) || null;
  }

  function stateOf(target) {
    const popup = getDialog(target);
    return popup ? dialogs.get(popup) : null;
  }

  function dialogFor(element) {
    // Dialog.Close links through context in Base UI; its port marker carries
    // the dialog id when the close sits outside the popup.
    const id =
      element.getAttribute("data-templ-controls") || element.getAttribute("data-templ-dialog-close");
    if (id) return getDialog(id);
    return getDialog(element);
  }

  function triggersFor(popup) {
    if (!popup.id) return [];
    return document.querySelectorAll(
      '[data-base-ui-click-trigger][data-templ-controls="' + popup.id + '"]',
    );
  }

  function isModal(state) {
    return state.popup.getAttribute("data-templ-modal") !== "false";
  }

  // ----- interaction type ----------------------------------------------------

  // FloatingFocusManager tracks the last pointer/keyboard interaction to pick
  // touch initial focus and keyboard-visible return focus.
  let lastInteractionType = "";
  document.addEventListener(
    "pointerdown",
    (event) => {
      lastInteractionType = event.pointerType || "mouse";
    },
    true,
  );
  document.addEventListener(
    "keydown",
    () => {
      lastInteractionType = "keyboard";
    },
    true,
  );

  // ----- aria wiring (useDialogTitle/-Description registration) --------------

  function wireAria(state) {
    const popup = state.popup;
    const title = popup.querySelector("[data-templ-dialog-title]");
    if (title) {
      if (!title.id) title.id = popup.id + "-title";
      popup.setAttribute("aria-labelledby", title.id);
    } else {
      popup.removeAttribute("aria-labelledby");
    }
    const description = popup.querySelector("[data-templ-dialog-description]");
    if (description) {
      if (!description.id) description.id = popup.id + "-description";
      popup.setAttribute("aria-describedby", description.id);
    } else {
      popup.removeAttribute("aria-describedby");
    }
  }

  // ----- transition lifecycle ------------------------------------------------

  // The parts that render the transition status, popup first.
  function partsOf(state) {
    return [state.popup, state.backdrop];
  }

  // ----- nested dialog bookkeeping ------------------------------------------

  // A dialog is nested when its hidden portal node was SSRd inside another
  // dialog's content — the DOM pendant of Base UI's parent DialogRootContext. The
  // relation is recorded at registration (see ensureDialog); parentOf resolves
  // it to the parent's live state.
  function parentOf(state) {
    const parentId = state.root._templParent;
    return parentId ? stateOf(parentId) : null;
  }

  function nestedOpenCount(state) {
    return openStack.filter((other) => {
      for (let p = parentOf(other); p; p = parentOf(p)) {
        if (p === state) return true;
      }
      return false;
    }).length;
  }

  function updateNestedAttributes() {
    openStack.forEach((state) => {
      const count = nestedOpenCount(state);
      state.popup.style.setProperty("--nested-dialogs", String(count));
      state.popup.toggleAttribute("data-nested-dialog-open", count > 0);
    });
  }

  function isTopmost(state) {
    return nestedOpenCount(state) === 0;
  }

  // ----- open / close --------------------------------------------------------

  // DialogTrigger renders aria-controls while the popup is open
  // (triggerPopupId).
  function updateTriggers(state, isOpen) {
    triggersFor(state.popup).forEach((trigger) => {
      trigger.setAttribute("aria-expanded", isOpen ? "true" : "false");
      trigger.toggleAttribute("data-popup-open", isOpen);
      if (isOpen) trigger.setAttribute("aria-controls", state.popup.id);
      else trigger.removeAttribute("aria-controls");
    });
  }

  // DialogPortal renders an InternalBackdrop for a modal dialog while it is
  // mounted: fixed over the viewport, inert while closing, and useDismiss
  // treats it as a backdrop.
  function createInternalBackdrop() {
    const backdrop = document.createElement("div");
    backdrop.setAttribute("role", "presentation");
    backdrop.setAttribute("data-base-ui-inert", "");
    backdrop.style.cssText = "position:fixed;inset:0;user-select:none;-webkit-user-select:none";
    return backdrop;
  }

  function openDialog(target, trigger) {
    const state = stateOf(target);
    if (!state || state.open) return;

    const popup = state.popup;
    state.openType = trigger ? lastInteractionType || "mouse" : null;
    state.trigger =
      trigger && trigger instanceof Element ? trigger : triggersFor(popup)[0] || null;

    state.open = true;
    openStack.push(state);
    updateNestedAttributes();
    startDismiss(state);

    window.templ.portal.render(state.root);
    state.root.hidden = false;
    if (isModal(state)) {
      state.internalBackdrop ??= createInternalBackdrop();
      state.internalBackdrop.inert = false;
      state.root.prepend(state.internalBackdrop);
    }

    wireAria(state);

    window.templ.transition.open(partsOf(state));

    if (isModal(state)) {
      state.releaseScroll = window.templ.scrollLock.acquire(popup);
    }

    updateTriggers(state, true);

    // DialogPopup's FloatingFocusManager, mounted until the exit animation
    // finished. Opened by touch the popup takes focus, so the virtual
    // keyboard stays closed (createDefaultInitialFocus).
    if (state.focus) {
      state.focus.open();
    } else {
      state.focus = window.templ.focusManager.useFloatingFocusManager({
        floating: popup,
        reference: state.trigger,
        triggers: triggersFor(popup),
        modal: isModal(state),
        openInteractionType: state.openType,
        initialFocus: (interactionType) => (interactionType === "touch" ? popup : true),
        restoreFocus: "popup",
        closeOnFocusOut: !popup.hasAttribute("data-templ-disable-pointer-dismissal"),
        onOpenChange: (open) => requestOpenChange(popup, open),
      });
    }
  }

  function closeDialog(target) {
    const state = stateOf(target);
    if (!state || !state.open) return;

    const popup = state.popup;
    state.open = false;
    stopDismiss(state);
    const index = openStack.indexOf(state);
    if (index !== -1) openStack.splice(index, 1);
    updateNestedAttributes();

    // Base UI order on open=false: the transition status flips to ending,
    // aria-hidden marking and the scroll lock release immediately, the
    // popup unmounts (and focus returns) once the exit animation finishes.
    state.focus?.close();
    if (state.internalBackdrop) state.internalBackdrop.inert = true;
    window.templ.transition.close(partsOf(state), popup, () => {
      state.root.hidden = true;
      popup.style.removeProperty("--nested-dialogs");
      popup.removeAttribute("data-nested-dialog-open");
      // Unmounting the focus manager returns focus.
      state.focus?.unmount();
      state.focus = null;
      state.internalBackdrop?.remove();
      // onOpenChangeComplete(false) pendant: fires once the exit animation
      // finished and the dialog unmounted (command.js resets its palette on
      // this).
      popup.dispatchEvent(new CustomEvent("dialog-close", { bubbles: true }));
    });
    state.releaseScroll?.();
    state.releaseScroll = null;
    updateTriggers(state, false);
  }

  function isDialogOpen(target) {
    return stateOf(target)?.open || false;
  }

  function requestOpenChange(target, nextOpen, trigger) {
    const state = stateOf(target);
    if (!state || state.open === nextOpen) return false;
    const accepted = state.popup.dispatchEvent(
      new CustomEvent("dialog-open-change", {
        bubbles: true,
        cancelable: true,
        detail: { open: nextOpen },
      }),
    );
    if (!accepted || state.popup.hasAttribute("data-templ-open")) return false;
    if (nextOpen) openDialog(state.popup, trigger);
    else closeDialog(state.popup);
    return true;
  }

  function toggleDialog(target, trigger) {
    requestOpenChange(target, !isDialogOpen(target), trigger);
  }

  // ----- dismissal (useDismiss, options from useDialogRoot) ------------------

  function startDismiss(state) {
    const popup = state.popup;
    state.dismiss = window.templ.dismiss.useDismiss({
      floating: popup,
      reference: triggersFor(popup),
      // A nested open dialog blocks its parent.
      escapeKey: () => isTopmost(state),
      // With a backdrop the dismissal waits for the click, so a press that
      // starts inside and is released over the backdrop never dismisses.
      // Modal is a boolean here, Base UI's "trap-focus" mode does not exist.
      outsidePressEvent: () => (state.internalBackdrop?.isConnected || state.backdrop) ? "intentional" : { mouse: "intentional", touch: "sloppy" },
      outsidePress(event) {
        if ("button" in event && event.button !== 0) return false;
        if ("touches" in event && event.touches.length !== 1) return false;
        if (!isTopmost(state) || popup.hasAttribute("data-templ-disable-pointer-dismissal")) return false;
        // A modal dialog closes only on its own backdrop, which supports
        // several modal dialogs that are not nested.
        if (!isModal(state)) return true;
        const target = event.target;
        const internalBackdrop = state.internalBackdrop?.isConnected ? state.internalBackdrop : null;
        if (!state.backdrop && !internalBackdrop) return true;
        return target === state.backdrop || target === internalBackdrop ||
          (target.contains(popup) && !target.hasAttribute("data-base-ui-portal"));
      },
      onOpenChange: (open) => requestOpenChange(popup, open),
    });
  }

  function stopDismiss(state) {
    state.dismiss?.();
    state.dismiss = null;
  }

  // ----- initialization ------------------------------------------------------

  function ensureDialog(popup) {
    if (!popup || dialogs.has(popup)) return dialogs.get(popup) || null;
    const root = popup.parentElement;

    const parentPopup = root.parentElement?.closest(POPUP);
    if (parentPopup?.id) root._templParent = parentPopup.id;

    const state = {
      root,
      popup,
      backdrop: root.querySelector(BACKDROP),
      open: false,
      trigger: null,
      openType: null,
      focus: null,
      internalBackdrop: null,
      releaseScroll: null,
    };
    dialogs.set(popup, state);

    // A nested dialog renders no backdrop in Base UI (DialogBackdrop's
    // enabled: !nested); the parent's backdrop keeps covering the page.
    if (root._templParent) {
      popup.setAttribute("data-nested", "");
      if (state.backdrop) state.backdrop.hidden = true;
    }

    wireAria(state);
    return state;
  }

  // Fully retire a dialog: undo aria-hidden marking, release the scroll
  // lock and remove the portaled DOM. Used when an htmx/datastar swap
  // removed the dialog's source from the page or replaced it with a fresh
  // hidden portal node.
  function destroyDialog(popup) {
    const state = dialogs.get(popup);
    if (!state) {
      popup.parentElement?.remove();
      return;
    }
    window.templ.transition.reset(partsOf(state), false);
    stopDismiss(state);
    state.focus?.unmount();
    state.focus = null;
    const index = openStack.indexOf(state);
    if (index !== -1) openStack.splice(index, 1);
    const wasOpen = state.open;
    state.open = false;
    updateNestedAttributes();
    state.releaseScroll?.();
    state.releaseScroll = null;
    if (wasOpen) popup.dispatchEvent(new CustomEvent("dialog-close", { bubbles: true }));
    window.templ.portal.remove(state.root);
    dialogs.delete(popup);
  }

  // DialogTrigger's useClick with its default click event. Base UI's
  // DialogTrigger identifier is shared with PopoverTrigger and DrawerTrigger;
  // only triggers whose data-templ-controls names a dialog popup are ours.
  window.templ.lifecycle.register("[data-base-ui-click-trigger][data-templ-controls]", {
    init(trigger) {
      if (!dialogFor(trigger)) return;
      trigger._templDialogClick = window.templ.click.useClick(trigger, {
        isOpen: () => isDialogOpen(dialogFor(trigger)),
        onOpenChange: (nextOpen) => requestOpenChange(dialogFor(trigger), nextOpen, trigger),
      });
    },
    destroy(trigger) {
      trigger._templDialogClick?.();
      trigger._templDialogClick = null;
    },
  });

  document.addEventListener("click", (event) => {
    if (!(event.target instanceof Element)) return;
    const closeButton = event.target.closest("[data-templ-dialog-close]");
    if (closeButton) {
      requestOpenChange(dialogFor(closeButton), false);
    }
  });

  // A dialog lives as long as its SSR declaration site (the portal owner)
  // stays in the document, including trigger-less programmatic dialogs.
  // Unmounting retires it: aria-hidden marking, scroll lock, portaled DOM.
  window.templ.lifecycle.register(POPUP, {
    init(popup) {
      if (!ensureDialog(popup)) return;
      // Server-side open state (Base UI open or defaultOpen).
      if (popup.getAttribute("data-templ-open") === "true" || popup.hasAttribute("data-templ-default-open")) {
        openDialog(popup);
      }
    },
    destroy: destroyDialog,
  });

  window.templ = window.templ || {};
  window.templ.dialog = {
    open: openDialog,
    close: closeDialog,
    toggle: toggleDialog,
    isOpen: isDialogOpen,
  };
})();

// components/dropdownmenu/dropdownmenu.js
(function () {
  // shadcn's dropdown menu on Base UI's Menu: a root menu with its MenuTrigger
  // (components/baseui/menu_root.js). Base UI's MenuTrigger renders no
  // identifier: a menu trigger is whatever has aria-haspopup="menu" and links
  // a menu popup (data-templ-controls). The element may carry another
  // component's slot (sidebar.MenuButton).
  window.templ.menuRoot.create({
    slot: "dropdown-menu",
    event: "dropdownmenu",
    trigger: '[aria-haspopup="menu"][data-templ-controls]',
    // shadcn's DropdownMenuSubContent.
    subPositioning: { side: "right", align: "start", sideOffset: 0, alignOffset: -3 },
  });
})();

// components/floatingui/floating_ui_core.js
// @floating-ui/core 1.7.5, dist/floating-ui.core.umd.js, the version @base-ui/react
// 1.6.0 resolves at the pin in plans/UPSTREAM.md.
(function (global, factory) {
  typeof exports === 'object' && typeof module !== 'undefined' ? factory(exports) :
  typeof define === 'function' && define.amd ? define(['exports'], factory) :
  (global = typeof globalThis !== 'undefined' ? globalThis : global || self, factory(global.FloatingUICore = {}));
})(this, (function (exports) { 'use strict';

  /**
   * Custom positioning reference element.
   * @see https://floating-ui.com/docs/virtual-elements
   */

  const sides = ['top', 'right', 'bottom', 'left'];
  const alignments = ['start', 'end'];
  const placements = /*#__PURE__*/sides.reduce((acc, side) => acc.concat(side, side + "-" + alignments[0], side + "-" + alignments[1]), []);
  const min = Math.min;
  const max = Math.max;
  const oppositeSideMap = {
    left: 'right',
    right: 'left',
    bottom: 'top',
    top: 'bottom'
  };
  function clamp(start, value, end) {
    return max(start, min(value, end));
  }
  function evaluate(value, param) {
    return typeof value === 'function' ? value(param) : value;
  }
  function getSide(placement) {
    return placement.split('-')[0];
  }
  function getAlignment(placement) {
    return placement.split('-')[1];
  }
  function getOppositeAxis(axis) {
    return axis === 'x' ? 'y' : 'x';
  }
  function getAxisLength(axis) {
    return axis === 'y' ? 'height' : 'width';
  }
  function getSideAxis(placement) {
    const firstChar = placement[0];
    return firstChar === 't' || firstChar === 'b' ? 'y' : 'x';
  }
  function getAlignmentAxis(placement) {
    return getOppositeAxis(getSideAxis(placement));
  }
  function getAlignmentSides(placement, rects, rtl) {
    if (rtl === void 0) {
      rtl = false;
    }
    const alignment = getAlignment(placement);
    const alignmentAxis = getAlignmentAxis(placement);
    const length = getAxisLength(alignmentAxis);
    let mainAlignmentSide = alignmentAxis === 'x' ? alignment === (rtl ? 'end' : 'start') ? 'right' : 'left' : alignment === 'start' ? 'bottom' : 'top';
    if (rects.reference[length] > rects.floating[length]) {
      mainAlignmentSide = getOppositePlacement(mainAlignmentSide);
    }
    return [mainAlignmentSide, getOppositePlacement(mainAlignmentSide)];
  }
  function getExpandedPlacements(placement) {
    const oppositePlacement = getOppositePlacement(placement);
    return [getOppositeAlignmentPlacement(placement), oppositePlacement, getOppositeAlignmentPlacement(oppositePlacement)];
  }
  function getOppositeAlignmentPlacement(placement) {
    return placement.includes('start') ? placement.replace('start', 'end') : placement.replace('end', 'start');
  }
  const lrPlacement = ['left', 'right'];
  const rlPlacement = ['right', 'left'];
  const tbPlacement = ['top', 'bottom'];
  const btPlacement = ['bottom', 'top'];
  function getSideList(side, isStart, rtl) {
    switch (side) {
      case 'top':
      case 'bottom':
        if (rtl) return isStart ? rlPlacement : lrPlacement;
        return isStart ? lrPlacement : rlPlacement;
      case 'left':
      case 'right':
        return isStart ? tbPlacement : btPlacement;
      default:
        return [];
    }
  }
  function getOppositeAxisPlacements(placement, flipAlignment, direction, rtl) {
    const alignment = getAlignment(placement);
    let list = getSideList(getSide(placement), direction === 'start', rtl);
    if (alignment) {
      list = list.map(side => side + "-" + alignment);
      if (flipAlignment) {
        list = list.concat(list.map(getOppositeAlignmentPlacement));
      }
    }
    return list;
  }
  function getOppositePlacement(placement) {
    const side = getSide(placement);
    return oppositeSideMap[side] + placement.slice(side.length);
  }
  function expandPaddingObject(padding) {
    return {
      top: 0,
      right: 0,
      bottom: 0,
      left: 0,
      ...padding
    };
  }
  function getPaddingObject(padding) {
    return typeof padding !== 'number' ? expandPaddingObject(padding) : {
      top: padding,
      right: padding,
      bottom: padding,
      left: padding
    };
  }
  function rectToClientRect(rect) {
    const {
      x,
      y,
      width,
      height
    } = rect;
    return {
      width,
      height,
      top: y,
      left: x,
      right: x + width,
      bottom: y + height,
      x,
      y
    };
  }

  function computeCoordsFromPlacement(_ref, placement, rtl) {
    let {
      reference,
      floating
    } = _ref;
    const sideAxis = getSideAxis(placement);
    const alignmentAxis = getAlignmentAxis(placement);
    const alignLength = getAxisLength(alignmentAxis);
    const side = getSide(placement);
    const isVertical = sideAxis === 'y';
    const commonX = reference.x + reference.width / 2 - floating.width / 2;
    const commonY = reference.y + reference.height / 2 - floating.height / 2;
    const commonAlign = reference[alignLength] / 2 - floating[alignLength] / 2;
    let coords;
    switch (side) {
      case 'top':
        coords = {
          x: commonX,
          y: reference.y - floating.height
        };
        break;
      case 'bottom':
        coords = {
          x: commonX,
          y: reference.y + reference.height
        };
        break;
      case 'right':
        coords = {
          x: reference.x + reference.width,
          y: commonY
        };
        break;
      case 'left':
        coords = {
          x: reference.x - floating.width,
          y: commonY
        };
        break;
      default:
        coords = {
          x: reference.x,
          y: reference.y
        };
    }
    switch (getAlignment(placement)) {
      case 'start':
        coords[alignmentAxis] -= commonAlign * (rtl && isVertical ? -1 : 1);
        break;
      case 'end':
        coords[alignmentAxis] += commonAlign * (rtl && isVertical ? -1 : 1);
        break;
    }
    return coords;
  }

  /**
   * Resolves with an object of overflow side offsets that determine how much the
   * element is overflowing a given clipping boundary on each side.
   * - positive = overflowing the boundary by that number of pixels
   * - negative = how many pixels left before it will overflow
   * - 0 = lies flush with the boundary
   * @see https://floating-ui.com/docs/detectOverflow
   */
  async function detectOverflow(state, options) {
    var _await$platform$isEle;
    if (options === void 0) {
      options = {};
    }
    const {
      x,
      y,
      platform,
      rects,
      elements,
      strategy
    } = state;
    const {
      boundary = 'clippingAncestors',
      rootBoundary = 'viewport',
      elementContext = 'floating',
      altBoundary = false,
      padding = 0
    } = evaluate(options, state);
    const paddingObject = getPaddingObject(padding);
    const altContext = elementContext === 'floating' ? 'reference' : 'floating';
    const element = elements[altBoundary ? altContext : elementContext];
    const clippingClientRect = rectToClientRect(await platform.getClippingRect({
      element: ((_await$platform$isEle = await (platform.isElement == null ? void 0 : platform.isElement(element))) != null ? _await$platform$isEle : true) ? element : element.contextElement || (await (platform.getDocumentElement == null ? void 0 : platform.getDocumentElement(elements.floating))),
      boundary,
      rootBoundary,
      strategy
    }));
    const rect = elementContext === 'floating' ? {
      x,
      y,
      width: rects.floating.width,
      height: rects.floating.height
    } : rects.reference;
    const offsetParent = await (platform.getOffsetParent == null ? void 0 : platform.getOffsetParent(elements.floating));
    const offsetScale = (await (platform.isElement == null ? void 0 : platform.isElement(offsetParent))) ? (await (platform.getScale == null ? void 0 : platform.getScale(offsetParent))) || {
      x: 1,
      y: 1
    } : {
      x: 1,
      y: 1
    };
    const elementClientRect = rectToClientRect(platform.convertOffsetParentRelativeRectToViewportRelativeRect ? await platform.convertOffsetParentRelativeRectToViewportRelativeRect({
      elements,
      rect,
      offsetParent,
      strategy
    }) : rect);
    return {
      top: (clippingClientRect.top - elementClientRect.top + paddingObject.top) / offsetScale.y,
      bottom: (elementClientRect.bottom - clippingClientRect.bottom + paddingObject.bottom) / offsetScale.y,
      left: (clippingClientRect.left - elementClientRect.left + paddingObject.left) / offsetScale.x,
      right: (elementClientRect.right - clippingClientRect.right + paddingObject.right) / offsetScale.x
    };
  }

  // Maximum number of resets that can occur before bailing to avoid infinite reset loops.
  const MAX_RESET_COUNT = 50;

  /**
   * Computes the `x` and `y` coordinates that will place the floating element
   * next to a given reference element.
   *
   * This export does not have any `platform` interface logic. You will need to
   * write one for the platform you are using Floating UI with.
   */
  const computePosition = async (reference, floating, config) => {
    const {
      placement = 'bottom',
      strategy = 'absolute',
      middleware = [],
      platform
    } = config;
    const platformWithDetectOverflow = platform.detectOverflow ? platform : {
      ...platform,
      detectOverflow
    };
    const rtl = await (platform.isRTL == null ? void 0 : platform.isRTL(floating));
    let rects = await platform.getElementRects({
      reference,
      floating,
      strategy
    });
    let {
      x,
      y
    } = computeCoordsFromPlacement(rects, placement, rtl);
    let statefulPlacement = placement;
    let resetCount = 0;
    const middlewareData = {};
    for (let i = 0; i < middleware.length; i++) {
      const currentMiddleware = middleware[i];
      if (!currentMiddleware) {
        continue;
      }
      const {
        name,
        fn
      } = currentMiddleware;
      const {
        x: nextX,
        y: nextY,
        data,
        reset
      } = await fn({
        x,
        y,
        initialPlacement: placement,
        placement: statefulPlacement,
        strategy,
        middlewareData,
        rects,
        platform: platformWithDetectOverflow,
        elements: {
          reference,
          floating
        }
      });
      x = nextX != null ? nextX : x;
      y = nextY != null ? nextY : y;
      middlewareData[name] = {
        ...middlewareData[name],
        ...data
      };
      if (reset && resetCount < MAX_RESET_COUNT) {
        resetCount++;
        if (typeof reset === 'object') {
          if (reset.placement) {
            statefulPlacement = reset.placement;
          }
          if (reset.rects) {
            rects = reset.rects === true ? await platform.getElementRects({
              reference,
              floating,
              strategy
            }) : reset.rects;
          }
          ({
            x,
            y
          } = computeCoordsFromPlacement(rects, statefulPlacement, rtl));
        }
        i = -1;
      }
    }
    return {
      x,
      y,
      placement: statefulPlacement,
      strategy,
      middlewareData
    };
  };

  /**
   * Provides data to position an inner element of the floating element so that it
   * appears centered to the reference element.
   * @see https://floating-ui.com/docs/arrow
   */
  const arrow = options => ({
    name: 'arrow',
    options,
    async fn(state) {
      const {
        x,
        y,
        placement,
        rects,
        platform,
        elements,
        middlewareData
      } = state;
      // Since `element` is required, we don't Partial<> the type.
      const {
        element,
        padding = 0
      } = evaluate(options, state) || {};
      if (element == null) {
        return {};
      }
      const paddingObject = getPaddingObject(padding);
      const coords = {
        x,
        y
      };
      const axis = getAlignmentAxis(placement);
      const length = getAxisLength(axis);
      const arrowDimensions = await platform.getDimensions(element);
      const isYAxis = axis === 'y';
      const minProp = isYAxis ? 'top' : 'left';
      const maxProp = isYAxis ? 'bottom' : 'right';
      const clientProp = isYAxis ? 'clientHeight' : 'clientWidth';
      const endDiff = rects.reference[length] + rects.reference[axis] - coords[axis] - rects.floating[length];
      const startDiff = coords[axis] - rects.reference[axis];
      const arrowOffsetParent = await (platform.getOffsetParent == null ? void 0 : platform.getOffsetParent(element));
      let clientSize = arrowOffsetParent ? arrowOffsetParent[clientProp] : 0;

      // DOM platform can return `window` as the `offsetParent`.
      if (!clientSize || !(await (platform.isElement == null ? void 0 : platform.isElement(arrowOffsetParent)))) {
        clientSize = elements.floating[clientProp] || rects.floating[length];
      }
      const centerToReference = endDiff / 2 - startDiff / 2;

      // If the padding is large enough that it causes the arrow to no longer be
      // centered, modify the padding so that it is centered.
      const largestPossiblePadding = clientSize / 2 - arrowDimensions[length] / 2 - 1;
      const minPadding = min(paddingObject[minProp], largestPossiblePadding);
      const maxPadding = min(paddingObject[maxProp], largestPossiblePadding);

      // Make sure the arrow doesn't overflow the floating element if the center
      // point is outside the floating element's bounds.
      const min$1 = minPadding;
      const max = clientSize - arrowDimensions[length] - maxPadding;
      const center = clientSize / 2 - arrowDimensions[length] / 2 + centerToReference;
      const offset = clamp(min$1, center, max);

      // If the reference is small enough that the arrow's padding causes it to
      // to point to nothing for an aligned placement, adjust the offset of the
      // floating element itself. To ensure `shift()` continues to take action,
      // a single reset is performed when this is true.
      const shouldAddOffset = !middlewareData.arrow && getAlignment(placement) != null && center !== offset && rects.reference[length] / 2 - (center < min$1 ? minPadding : maxPadding) - arrowDimensions[length] / 2 < 0;
      const alignmentOffset = shouldAddOffset ? center < min$1 ? center - min$1 : center - max : 0;
      return {
        [axis]: coords[axis] + alignmentOffset,
        data: {
          [axis]: offset,
          centerOffset: center - offset - alignmentOffset,
          ...(shouldAddOffset && {
            alignmentOffset
          })
        },
        reset: shouldAddOffset
      };
    }
  });

  function getPlacementList(alignment, autoAlignment, allowedPlacements) {
    const allowedPlacementsSortedByAlignment = alignment ? [...allowedPlacements.filter(placement => getAlignment(placement) === alignment), ...allowedPlacements.filter(placement => getAlignment(placement) !== alignment)] : allowedPlacements.filter(placement => getSide(placement) === placement);
    return allowedPlacementsSortedByAlignment.filter(placement => {
      if (alignment) {
        return getAlignment(placement) === alignment || (autoAlignment ? getOppositeAlignmentPlacement(placement) !== placement : false);
      }
      return true;
    });
  }
  /**
   * Optimizes the visibility of the floating element by choosing the placement
   * that has the most space available automatically, without needing to specify a
   * preferred placement. Alternative to `flip`.
   * @see https://floating-ui.com/docs/autoPlacement
   */
  const autoPlacement = function (options) {
    if (options === void 0) {
      options = {};
    }
    return {
      name: 'autoPlacement',
      options,
      async fn(state) {
        var _middlewareData$autoP, _middlewareData$autoP2, _placementsThatFitOnE;
        const {
          rects,
          middlewareData,
          placement,
          platform,
          elements
        } = state;
        const {
          crossAxis = false,
          alignment,
          allowedPlacements = placements,
          autoAlignment = true,
          ...detectOverflowOptions
        } = evaluate(options, state);
        const placements$1 = alignment !== undefined || allowedPlacements === placements ? getPlacementList(alignment || null, autoAlignment, allowedPlacements) : allowedPlacements;
        const overflow = await platform.detectOverflow(state, detectOverflowOptions);
        const currentIndex = ((_middlewareData$autoP = middlewareData.autoPlacement) == null ? void 0 : _middlewareData$autoP.index) || 0;
        const currentPlacement = placements$1[currentIndex];
        if (currentPlacement == null) {
          return {};
        }
        const alignmentSides = getAlignmentSides(currentPlacement, rects, await (platform.isRTL == null ? void 0 : platform.isRTL(elements.floating)));

        // Make `computeCoords` start from the right place.
        if (placement !== currentPlacement) {
          return {
            reset: {
              placement: placements$1[0]
            }
          };
        }
        const currentOverflows = [overflow[getSide(currentPlacement)], overflow[alignmentSides[0]], overflow[alignmentSides[1]]];
        const allOverflows = [...(((_middlewareData$autoP2 = middlewareData.autoPlacement) == null ? void 0 : _middlewareData$autoP2.overflows) || []), {
          placement: currentPlacement,
          overflows: currentOverflows
        }];
        const nextPlacement = placements$1[currentIndex + 1];

        // There are more placements to check.
        if (nextPlacement) {
          return {
            data: {
              index: currentIndex + 1,
              overflows: allOverflows
            },
            reset: {
              placement: nextPlacement
            }
          };
        }
        const placementsSortedByMostSpace = allOverflows.map(d => {
          const alignment = getAlignment(d.placement);
          return [d.placement, alignment && crossAxis ?
          // Check along the mainAxis and main crossAxis side.
          d.overflows.slice(0, 2).reduce((acc, v) => acc + v, 0) :
          // Check only the mainAxis.
          d.overflows[0], d.overflows];
        }).sort((a, b) => a[1] - b[1]);
        const placementsThatFitOnEachSide = placementsSortedByMostSpace.filter(d => d[2].slice(0,
        // Aligned placements should not check their opposite crossAxis
        // side.
        getAlignment(d[0]) ? 2 : 3).every(v => v <= 0));
        const resetPlacement = ((_placementsThatFitOnE = placementsThatFitOnEachSide[0]) == null ? void 0 : _placementsThatFitOnE[0]) || placementsSortedByMostSpace[0][0];
        if (resetPlacement !== placement) {
          return {
            data: {
              index: currentIndex + 1,
              overflows: allOverflows
            },
            reset: {
              placement: resetPlacement
            }
          };
        }
        return {};
      }
    };
  };

  /**
   * Optimizes the visibility of the floating element by flipping the `placement`
   * in order to keep it in view when the preferred placement(s) will overflow the
   * clipping boundary. Alternative to `autoPlacement`.
   * @see https://floating-ui.com/docs/flip
   */
  const flip = function (options) {
    if (options === void 0) {
      options = {};
    }
    return {
      name: 'flip',
      options,
      async fn(state) {
        var _middlewareData$arrow, _middlewareData$flip;
        const {
          placement,
          middlewareData,
          rects,
          initialPlacement,
          platform,
          elements
        } = state;
        const {
          mainAxis: checkMainAxis = true,
          crossAxis: checkCrossAxis = true,
          fallbackPlacements: specifiedFallbackPlacements,
          fallbackStrategy = 'bestFit',
          fallbackAxisSideDirection = 'none',
          flipAlignment = true,
          ...detectOverflowOptions
        } = evaluate(options, state);

        // If a reset by the arrow was caused due to an alignment offset being
        // added, we should skip any logic now since `flip()` has already done its
        // work.
        // https://github.com/floating-ui/floating-ui/issues/2549#issuecomment-1719601643
        if ((_middlewareData$arrow = middlewareData.arrow) != null && _middlewareData$arrow.alignmentOffset) {
          return {};
        }
        const side = getSide(placement);
        const initialSideAxis = getSideAxis(initialPlacement);
        const isBasePlacement = getSide(initialPlacement) === initialPlacement;
        const rtl = await (platform.isRTL == null ? void 0 : platform.isRTL(elements.floating));
        const fallbackPlacements = specifiedFallbackPlacements || (isBasePlacement || !flipAlignment ? [getOppositePlacement(initialPlacement)] : getExpandedPlacements(initialPlacement));
        const hasFallbackAxisSideDirection = fallbackAxisSideDirection !== 'none';
        if (!specifiedFallbackPlacements && hasFallbackAxisSideDirection) {
          fallbackPlacements.push(...getOppositeAxisPlacements(initialPlacement, flipAlignment, fallbackAxisSideDirection, rtl));
        }
        const placements = [initialPlacement, ...fallbackPlacements];
        const overflow = await platform.detectOverflow(state, detectOverflowOptions);
        const overflows = [];
        let overflowsData = ((_middlewareData$flip = middlewareData.flip) == null ? void 0 : _middlewareData$flip.overflows) || [];
        if (checkMainAxis) {
          overflows.push(overflow[side]);
        }
        if (checkCrossAxis) {
          const sides = getAlignmentSides(placement, rects, rtl);
          overflows.push(overflow[sides[0]], overflow[sides[1]]);
        }
        overflowsData = [...overflowsData, {
          placement,
          overflows
        }];

        // One or more sides is overflowing.
        if (!overflows.every(side => side <= 0)) {
          var _middlewareData$flip2, _overflowsData$filter;
          const nextIndex = (((_middlewareData$flip2 = middlewareData.flip) == null ? void 0 : _middlewareData$flip2.index) || 0) + 1;
          const nextPlacement = placements[nextIndex];
          if (nextPlacement) {
            const ignoreCrossAxisOverflow = checkCrossAxis === 'alignment' ? initialSideAxis !== getSideAxis(nextPlacement) : false;
            if (!ignoreCrossAxisOverflow ||
            // We leave the current main axis only if every placement on that axis
            // overflows the main axis.
            overflowsData.every(d => getSideAxis(d.placement) === initialSideAxis ? d.overflows[0] > 0 : true)) {
              // Try next placement and re-run the lifecycle.
              return {
                data: {
                  index: nextIndex,
                  overflows: overflowsData
                },
                reset: {
                  placement: nextPlacement
                }
              };
            }
          }

          // First, find the candidates that fit on the mainAxis side of overflow,
          // then find the placement that fits the best on the main crossAxis side.
          let resetPlacement = (_overflowsData$filter = overflowsData.filter(d => d.overflows[0] <= 0).sort((a, b) => a.overflows[1] - b.overflows[1])[0]) == null ? void 0 : _overflowsData$filter.placement;

          // Otherwise fallback.
          if (!resetPlacement) {
            switch (fallbackStrategy) {
              case 'bestFit':
                {
                  var _overflowsData$filter2;
                  const placement = (_overflowsData$filter2 = overflowsData.filter(d => {
                    if (hasFallbackAxisSideDirection) {
                      const currentSideAxis = getSideAxis(d.placement);
                      return currentSideAxis === initialSideAxis ||
                      // Create a bias to the `y` side axis due to horizontal
                      // reading directions favoring greater width.
                      currentSideAxis === 'y';
                    }
                    return true;
                  }).map(d => [d.placement, d.overflows.filter(overflow => overflow > 0).reduce((acc, overflow) => acc + overflow, 0)]).sort((a, b) => a[1] - b[1])[0]) == null ? void 0 : _overflowsData$filter2[0];
                  if (placement) {
                    resetPlacement = placement;
                  }
                  break;
                }
              case 'initialPlacement':
                resetPlacement = initialPlacement;
                break;
            }
          }
          if (placement !== resetPlacement) {
            return {
              reset: {
                placement: resetPlacement
              }
            };
          }
        }
        return {};
      }
    };
  };

  function getSideOffsets(overflow, rect) {
    return {
      top: overflow.top - rect.height,
      right: overflow.right - rect.width,
      bottom: overflow.bottom - rect.height,
      left: overflow.left - rect.width
    };
  }
  function isAnySideFullyClipped(overflow) {
    return sides.some(side => overflow[side] >= 0);
  }
  /**
   * Provides data to hide the floating element in applicable situations, such as
   * when it is not in the same clipping context as the reference element.
   * @see https://floating-ui.com/docs/hide
   */
  const hide = function (options) {
    if (options === void 0) {
      options = {};
    }
    return {
      name: 'hide',
      options,
      async fn(state) {
        const {
          rects,
          platform
        } = state;
        const {
          strategy = 'referenceHidden',
          ...detectOverflowOptions
        } = evaluate(options, state);
        switch (strategy) {
          case 'referenceHidden':
            {
              const overflow = await platform.detectOverflow(state, {
                ...detectOverflowOptions,
                elementContext: 'reference'
              });
              const offsets = getSideOffsets(overflow, rects.reference);
              return {
                data: {
                  referenceHiddenOffsets: offsets,
                  referenceHidden: isAnySideFullyClipped(offsets)
                }
              };
            }
          case 'escaped':
            {
              const overflow = await platform.detectOverflow(state, {
                ...detectOverflowOptions,
                altBoundary: true
              });
              const offsets = getSideOffsets(overflow, rects.floating);
              return {
                data: {
                  escapedOffsets: offsets,
                  escaped: isAnySideFullyClipped(offsets)
                }
              };
            }
          default:
            {
              return {};
            }
        }
      }
    };
  };

  function getBoundingRect(rects) {
    const minX = min(...rects.map(rect => rect.left));
    const minY = min(...rects.map(rect => rect.top));
    const maxX = max(...rects.map(rect => rect.right));
    const maxY = max(...rects.map(rect => rect.bottom));
    return {
      x: minX,
      y: minY,
      width: maxX - minX,
      height: maxY - minY
    };
  }
  function getRectsByLine(rects) {
    const sortedRects = rects.slice().sort((a, b) => a.y - b.y);
    const groups = [];
    let prevRect = null;
    for (let i = 0; i < sortedRects.length; i++) {
      const rect = sortedRects[i];
      if (!prevRect || rect.y - prevRect.y > prevRect.height / 2) {
        groups.push([rect]);
      } else {
        groups[groups.length - 1].push(rect);
      }
      prevRect = rect;
    }
    return groups.map(rect => rectToClientRect(getBoundingRect(rect)));
  }
  /**
   * Provides improved positioning for inline reference elements that can span
   * over multiple lines, such as hyperlinks or range selections.
   * @see https://floating-ui.com/docs/inline
   */
  const inline = function (options) {
    if (options === void 0) {
      options = {};
    }
    return {
      name: 'inline',
      options,
      async fn(state) {
        const {
          placement,
          elements,
          rects,
          platform,
          strategy
        } = state;
        // A MouseEvent's client{X,Y} coords can be up to 2 pixels off a
        // ClientRect's bounds, despite the event listener being triggered. A
        // padding of 2 seems to handle this issue.
        const {
          padding = 2,
          x,
          y
        } = evaluate(options, state);
        const nativeClientRects = Array.from((await (platform.getClientRects == null ? void 0 : platform.getClientRects(elements.reference))) || []);
        const clientRects = getRectsByLine(nativeClientRects);
        const fallback = rectToClientRect(getBoundingRect(nativeClientRects));
        const paddingObject = getPaddingObject(padding);
        function getBoundingClientRect() {
          // There are two rects and they are disjoined.
          if (clientRects.length === 2 && clientRects[0].left > clientRects[1].right && x != null && y != null) {
            // Find the first rect in which the point is fully inside.
            return clientRects.find(rect => x > rect.left - paddingObject.left && x < rect.right + paddingObject.right && y > rect.top - paddingObject.top && y < rect.bottom + paddingObject.bottom) || fallback;
          }

          // There are 2 or more connected rects.
          if (clientRects.length >= 2) {
            if (getSideAxis(placement) === 'y') {
              const firstRect = clientRects[0];
              const lastRect = clientRects[clientRects.length - 1];
              const isTop = getSide(placement) === 'top';
              const top = firstRect.top;
              const bottom = lastRect.bottom;
              const left = isTop ? firstRect.left : lastRect.left;
              const right = isTop ? firstRect.right : lastRect.right;
              const width = right - left;
              const height = bottom - top;
              return {
                top,
                bottom,
                left,
                right,
                width,
                height,
                x: left,
                y: top
              };
            }
            const isLeftSide = getSide(placement) === 'left';
            const maxRight = max(...clientRects.map(rect => rect.right));
            const minLeft = min(...clientRects.map(rect => rect.left));
            const measureRects = clientRects.filter(rect => isLeftSide ? rect.left === minLeft : rect.right === maxRight);
            const top = measureRects[0].top;
            const bottom = measureRects[measureRects.length - 1].bottom;
            const left = minLeft;
            const right = maxRight;
            const width = right - left;
            const height = bottom - top;
            return {
              top,
              bottom,
              left,
              right,
              width,
              height,
              x: left,
              y: top
            };
          }
          return fallback;
        }
        const resetRects = await platform.getElementRects({
          reference: {
            getBoundingClientRect
          },
          floating: elements.floating,
          strategy
        });
        if (rects.reference.x !== resetRects.reference.x || rects.reference.y !== resetRects.reference.y || rects.reference.width !== resetRects.reference.width || rects.reference.height !== resetRects.reference.height) {
          return {
            reset: {
              rects: resetRects
            }
          };
        }
        return {};
      }
    };
  };

  const originSides = /*#__PURE__*/new Set(['left', 'top']);

  // For type backwards-compatibility, the `OffsetOptions` type was also
  // Derivable.

  async function convertValueToCoords(state, options) {
    const {
      placement,
      platform,
      elements
    } = state;
    const rtl = await (platform.isRTL == null ? void 0 : platform.isRTL(elements.floating));
    const side = getSide(placement);
    const alignment = getAlignment(placement);
    const isVertical = getSideAxis(placement) === 'y';
    const mainAxisMulti = originSides.has(side) ? -1 : 1;
    const crossAxisMulti = rtl && isVertical ? -1 : 1;
    const rawValue = evaluate(options, state);

    // eslint-disable-next-line prefer-const
    let {
      mainAxis,
      crossAxis,
      alignmentAxis
    } = typeof rawValue === 'number' ? {
      mainAxis: rawValue,
      crossAxis: 0,
      alignmentAxis: null
    } : {
      mainAxis: rawValue.mainAxis || 0,
      crossAxis: rawValue.crossAxis || 0,
      alignmentAxis: rawValue.alignmentAxis
    };
    if (alignment && typeof alignmentAxis === 'number') {
      crossAxis = alignment === 'end' ? alignmentAxis * -1 : alignmentAxis;
    }
    return isVertical ? {
      x: crossAxis * crossAxisMulti,
      y: mainAxis * mainAxisMulti
    } : {
      x: mainAxis * mainAxisMulti,
      y: crossAxis * crossAxisMulti
    };
  }

  /**
   * Modifies the placement by translating the floating element along the
   * specified axes.
   * A number (shorthand for `mainAxis` or distance), or an axes configuration
   * object may be passed.
   * @see https://floating-ui.com/docs/offset
   */
  const offset = function (options) {
    if (options === void 0) {
      options = 0;
    }
    return {
      name: 'offset',
      options,
      async fn(state) {
        var _middlewareData$offse, _middlewareData$arrow;
        const {
          x,
          y,
          placement,
          middlewareData
        } = state;
        const diffCoords = await convertValueToCoords(state, options);

        // If the placement is the same and the arrow caused an alignment offset
        // then we don't need to change the positioning coordinates.
        if (placement === ((_middlewareData$offse = middlewareData.offset) == null ? void 0 : _middlewareData$offse.placement) && (_middlewareData$arrow = middlewareData.arrow) != null && _middlewareData$arrow.alignmentOffset) {
          return {};
        }
        return {
          x: x + diffCoords.x,
          y: y + diffCoords.y,
          data: {
            ...diffCoords,
            placement
          }
        };
      }
    };
  };

  /**
   * Optimizes the visibility of the floating element by shifting it in order to
   * keep it in view when it will overflow the clipping boundary.
   * @see https://floating-ui.com/docs/shift
   */
  const shift = function (options) {
    if (options === void 0) {
      options = {};
    }
    return {
      name: 'shift',
      options,
      async fn(state) {
        const {
          x,
          y,
          placement,
          platform
        } = state;
        const {
          mainAxis: checkMainAxis = true,
          crossAxis: checkCrossAxis = false,
          limiter = {
            fn: _ref => {
              let {
                x,
                y
              } = _ref;
              return {
                x,
                y
              };
            }
          },
          ...detectOverflowOptions
        } = evaluate(options, state);
        const coords = {
          x,
          y
        };
        const overflow = await platform.detectOverflow(state, detectOverflowOptions);
        const crossAxis = getSideAxis(getSide(placement));
        const mainAxis = getOppositeAxis(crossAxis);
        let mainAxisCoord = coords[mainAxis];
        let crossAxisCoord = coords[crossAxis];
        if (checkMainAxis) {
          const minSide = mainAxis === 'y' ? 'top' : 'left';
          const maxSide = mainAxis === 'y' ? 'bottom' : 'right';
          const min = mainAxisCoord + overflow[minSide];
          const max = mainAxisCoord - overflow[maxSide];
          mainAxisCoord = clamp(min, mainAxisCoord, max);
        }
        if (checkCrossAxis) {
          const minSide = crossAxis === 'y' ? 'top' : 'left';
          const maxSide = crossAxis === 'y' ? 'bottom' : 'right';
          const min = crossAxisCoord + overflow[minSide];
          const max = crossAxisCoord - overflow[maxSide];
          crossAxisCoord = clamp(min, crossAxisCoord, max);
        }
        const limitedCoords = limiter.fn({
          ...state,
          [mainAxis]: mainAxisCoord,
          [crossAxis]: crossAxisCoord
        });
        return {
          ...limitedCoords,
          data: {
            x: limitedCoords.x - x,
            y: limitedCoords.y - y,
            enabled: {
              [mainAxis]: checkMainAxis,
              [crossAxis]: checkCrossAxis
            }
          }
        };
      }
    };
  };
  /**
   * Built-in `limiter` that will stop `shift()` at a certain point.
   */
  const limitShift = function (options) {
    if (options === void 0) {
      options = {};
    }
    return {
      options,
      fn(state) {
        const {
          x,
          y,
          placement,
          rects,
          middlewareData
        } = state;
        const {
          offset = 0,
          mainAxis: checkMainAxis = true,
          crossAxis: checkCrossAxis = true
        } = evaluate(options, state);
        const coords = {
          x,
          y
        };
        const crossAxis = getSideAxis(placement);
        const mainAxis = getOppositeAxis(crossAxis);
        let mainAxisCoord = coords[mainAxis];
        let crossAxisCoord = coords[crossAxis];
        const rawOffset = evaluate(offset, state);
        const computedOffset = typeof rawOffset === 'number' ? {
          mainAxis: rawOffset,
          crossAxis: 0
        } : {
          mainAxis: 0,
          crossAxis: 0,
          ...rawOffset
        };
        if (checkMainAxis) {
          const len = mainAxis === 'y' ? 'height' : 'width';
          const limitMin = rects.reference[mainAxis] - rects.floating[len] + computedOffset.mainAxis;
          const limitMax = rects.reference[mainAxis] + rects.reference[len] - computedOffset.mainAxis;
          if (mainAxisCoord < limitMin) {
            mainAxisCoord = limitMin;
          } else if (mainAxisCoord > limitMax) {
            mainAxisCoord = limitMax;
          }
        }
        if (checkCrossAxis) {
          var _middlewareData$offse, _middlewareData$offse2;
          const len = mainAxis === 'y' ? 'width' : 'height';
          const isOriginSide = originSides.has(getSide(placement));
          const limitMin = rects.reference[crossAxis] - rects.floating[len] + (isOriginSide ? ((_middlewareData$offse = middlewareData.offset) == null ? void 0 : _middlewareData$offse[crossAxis]) || 0 : 0) + (isOriginSide ? 0 : computedOffset.crossAxis);
          const limitMax = rects.reference[crossAxis] + rects.reference[len] + (isOriginSide ? 0 : ((_middlewareData$offse2 = middlewareData.offset) == null ? void 0 : _middlewareData$offse2[crossAxis]) || 0) - (isOriginSide ? computedOffset.crossAxis : 0);
          if (crossAxisCoord < limitMin) {
            crossAxisCoord = limitMin;
          } else if (crossAxisCoord > limitMax) {
            crossAxisCoord = limitMax;
          }
        }
        return {
          [mainAxis]: mainAxisCoord,
          [crossAxis]: crossAxisCoord
        };
      }
    };
  };

  /**
   * Provides data that allows you to change the size of the floating element —
   * for instance, prevent it from overflowing the clipping boundary or match the
   * width of the reference element.
   * @see https://floating-ui.com/docs/size
   */
  const size = function (options) {
    if (options === void 0) {
      options = {};
    }
    return {
      name: 'size',
      options,
      async fn(state) {
        var _state$middlewareData, _state$middlewareData2;
        const {
          placement,
          rects,
          platform,
          elements
        } = state;
        const {
          apply = () => {},
          ...detectOverflowOptions
        } = evaluate(options, state);
        const overflow = await platform.detectOverflow(state, detectOverflowOptions);
        const side = getSide(placement);
        const alignment = getAlignment(placement);
        const isYAxis = getSideAxis(placement) === 'y';
        const {
          width,
          height
        } = rects.floating;
        let heightSide;
        let widthSide;
        if (side === 'top' || side === 'bottom') {
          heightSide = side;
          widthSide = alignment === ((await (platform.isRTL == null ? void 0 : platform.isRTL(elements.floating))) ? 'start' : 'end') ? 'left' : 'right';
        } else {
          widthSide = side;
          heightSide = alignment === 'end' ? 'top' : 'bottom';
        }
        const maximumClippingHeight = height - overflow.top - overflow.bottom;
        const maximumClippingWidth = width - overflow.left - overflow.right;
        const overflowAvailableHeight = min(height - overflow[heightSide], maximumClippingHeight);
        const overflowAvailableWidth = min(width - overflow[widthSide], maximumClippingWidth);
        const noShift = !state.middlewareData.shift;
        let availableHeight = overflowAvailableHeight;
        let availableWidth = overflowAvailableWidth;
        if ((_state$middlewareData = state.middlewareData.shift) != null && _state$middlewareData.enabled.x) {
          availableWidth = maximumClippingWidth;
        }
        if ((_state$middlewareData2 = state.middlewareData.shift) != null && _state$middlewareData2.enabled.y) {
          availableHeight = maximumClippingHeight;
        }
        if (noShift && !alignment) {
          const xMin = max(overflow.left, 0);
          const xMax = max(overflow.right, 0);
          const yMin = max(overflow.top, 0);
          const yMax = max(overflow.bottom, 0);
          if (isYAxis) {
            availableWidth = width - 2 * (xMin !== 0 || xMax !== 0 ? xMin + xMax : max(overflow.left, overflow.right));
          } else {
            availableHeight = height - 2 * (yMin !== 0 || yMax !== 0 ? yMin + yMax : max(overflow.top, overflow.bottom));
          }
        }
        await apply({
          ...state,
          availableWidth,
          availableHeight
        });
        const nextDimensions = await platform.getDimensions(elements.floating);
        if (width !== nextDimensions.width || height !== nextDimensions.height) {
          return {
            reset: {
              rects: true
            }
          };
        }
        return {};
      }
    };
  };

  exports.arrow = arrow;
  exports.autoPlacement = autoPlacement;
  exports.computePosition = computePosition;
  exports.detectOverflow = detectOverflow;
  exports.flip = flip;
  exports.hide = hide;
  exports.inline = inline;
  exports.limitShift = limitShift;
  exports.offset = offset;
  exports.rectToClientRect = rectToClientRect;
  exports.shift = shift;
  exports.size = size;

}));

// components/floatingui/floating_ui_dom.js
// @floating-ui/dom 1.7.6, dist/floating-ui.dom.umd.js, the version @base-ui/react
// 1.6.0 resolves at the pin in plans/UPSTREAM.md.
(function (global, factory) {
  typeof exports === 'object' && typeof module !== 'undefined' ? factory(exports, require('@floating-ui/core')) :
  typeof define === 'function' && define.amd ? define(['exports', '@floating-ui/core'], factory) :
  (global = typeof globalThis !== 'undefined' ? globalThis : global || self, factory(global.FloatingUIDOM = {}, global.FloatingUICore));
})(this, (function (exports, core) { 'use strict';

  /**
   * Custom positioning reference element.
   * @see https://floating-ui.com/docs/virtual-elements
   */

  const min = Math.min;
  const max = Math.max;
  const round = Math.round;
  const floor = Math.floor;
  const createCoords = v => ({
    x: v,
    y: v
  });

  function hasWindow() {
    return typeof window !== 'undefined';
  }
  function getNodeName(node) {
    if (isNode(node)) {
      return (node.nodeName || '').toLowerCase();
    }
    // Mocked nodes in testing environments may not be instances of Node. By
    // returning `#document` an infinite loop won't occur.
    // https://github.com/floating-ui/floating-ui/issues/2317
    return '#document';
  }
  function getWindow(node) {
    var _node$ownerDocument;
    return (node == null || (_node$ownerDocument = node.ownerDocument) == null ? void 0 : _node$ownerDocument.defaultView) || window;
  }
  function getDocumentElement(node) {
    var _ref;
    return (_ref = (isNode(node) ? node.ownerDocument : node.document) || window.document) == null ? void 0 : _ref.documentElement;
  }
  function isNode(value) {
    if (!hasWindow()) {
      return false;
    }
    return value instanceof Node || value instanceof getWindow(value).Node;
  }
  function isElement(value) {
    if (!hasWindow()) {
      return false;
    }
    return value instanceof Element || value instanceof getWindow(value).Element;
  }
  function isHTMLElement(value) {
    if (!hasWindow()) {
      return false;
    }
    return value instanceof HTMLElement || value instanceof getWindow(value).HTMLElement;
  }
  function isShadowRoot(value) {
    if (!hasWindow() || typeof ShadowRoot === 'undefined') {
      return false;
    }
    return value instanceof ShadowRoot || value instanceof getWindow(value).ShadowRoot;
  }
  function isOverflowElement(element) {
    const {
      overflow,
      overflowX,
      overflowY,
      display
    } = getComputedStyle$1(element);
    return /auto|scroll|overlay|hidden|clip/.test(overflow + overflowY + overflowX) && display !== 'inline' && display !== 'contents';
  }
  function isTableElement(element) {
    return /^(table|td|th)$/.test(getNodeName(element));
  }
  function isTopLayer(element) {
    try {
      if (element.matches(':popover-open')) {
        return true;
      }
    } catch (_e) {
      // no-op
    }
    try {
      return element.matches(':modal');
    } catch (_e) {
      return false;
    }
  }
  const willChangeRe = /transform|translate|scale|rotate|perspective|filter/;
  const containRe = /paint|layout|strict|content/;
  const isNotNone = value => !!value && value !== 'none';
  let isWebKitValue;
  function isContainingBlock(elementOrCss) {
    const css = isElement(elementOrCss) ? getComputedStyle$1(elementOrCss) : elementOrCss;

    // https://developer.mozilla.org/en-US/docs/Web/CSS/Containing_block#identifying_the_containing_block
    // https://drafts.csswg.org/css-transforms-2/#individual-transforms
    return isNotNone(css.transform) || isNotNone(css.translate) || isNotNone(css.scale) || isNotNone(css.rotate) || isNotNone(css.perspective) || !isWebKit() && (isNotNone(css.backdropFilter) || isNotNone(css.filter)) || willChangeRe.test(css.willChange || '') || containRe.test(css.contain || '');
  }
  function getContainingBlock(element) {
    let currentNode = getParentNode(element);
    while (isHTMLElement(currentNode) && !isLastTraversableNode(currentNode)) {
      if (isContainingBlock(currentNode)) {
        return currentNode;
      } else if (isTopLayer(currentNode)) {
        return null;
      }
      currentNode = getParentNode(currentNode);
    }
    return null;
  }
  function isWebKit() {
    if (isWebKitValue == null) {
      isWebKitValue = typeof CSS !== 'undefined' && CSS.supports && CSS.supports('-webkit-backdrop-filter', 'none');
    }
    return isWebKitValue;
  }
  function isLastTraversableNode(node) {
    return /^(html|body|#document)$/.test(getNodeName(node));
  }
  function getComputedStyle$1(element) {
    return getWindow(element).getComputedStyle(element);
  }
  function getNodeScroll(element) {
    if (isElement(element)) {
      return {
        scrollLeft: element.scrollLeft,
        scrollTop: element.scrollTop
      };
    }
    return {
      scrollLeft: element.scrollX,
      scrollTop: element.scrollY
    };
  }
  function getParentNode(node) {
    if (getNodeName(node) === 'html') {
      return node;
    }
    const result =
    // Step into the shadow DOM of the parent of a slotted node.
    node.assignedSlot ||
    // DOM Element detected.
    node.parentNode ||
    // ShadowRoot detected.
    isShadowRoot(node) && node.host ||
    // Fallback.
    getDocumentElement(node);
    return isShadowRoot(result) ? result.host : result;
  }
  function getNearestOverflowAncestor(node) {
    const parentNode = getParentNode(node);
    if (isLastTraversableNode(parentNode)) {
      return node.ownerDocument ? node.ownerDocument.body : node.body;
    }
    if (isHTMLElement(parentNode) && isOverflowElement(parentNode)) {
      return parentNode;
    }
    return getNearestOverflowAncestor(parentNode);
  }
  function getOverflowAncestors(node, list, traverseIframes) {
    var _node$ownerDocument2;
    if (list === void 0) {
      list = [];
    }
    if (traverseIframes === void 0) {
      traverseIframes = true;
    }
    const scrollableAncestor = getNearestOverflowAncestor(node);
    const isBody = scrollableAncestor === ((_node$ownerDocument2 = node.ownerDocument) == null ? void 0 : _node$ownerDocument2.body);
    const win = getWindow(scrollableAncestor);
    if (isBody) {
      const frameElement = getFrameElement(win);
      return list.concat(win, win.visualViewport || [], isOverflowElement(scrollableAncestor) ? scrollableAncestor : [], frameElement && traverseIframes ? getOverflowAncestors(frameElement) : []);
    } else {
      return list.concat(scrollableAncestor, getOverflowAncestors(scrollableAncestor, [], traverseIframes));
    }
  }
  function getFrameElement(win) {
    return win.parent && Object.getPrototypeOf(win.parent) ? win.frameElement : null;
  }

  function getCssDimensions(element) {
    const css = getComputedStyle$1(element);
    // In testing environments, the `width` and `height` properties are empty
    // strings for SVG elements, returning NaN. Fallback to `0` in this case.
    let width = parseFloat(css.width) || 0;
    let height = parseFloat(css.height) || 0;
    const hasOffset = isHTMLElement(element);
    const offsetWidth = hasOffset ? element.offsetWidth : width;
    const offsetHeight = hasOffset ? element.offsetHeight : height;
    const shouldFallback = round(width) !== offsetWidth || round(height) !== offsetHeight;
    if (shouldFallback) {
      width = offsetWidth;
      height = offsetHeight;
    }
    return {
      width,
      height,
      $: shouldFallback
    };
  }

  function unwrapElement(element) {
    return !isElement(element) ? element.contextElement : element;
  }

  function getScale(element) {
    const domElement = unwrapElement(element);
    if (!isHTMLElement(domElement)) {
      return createCoords(1);
    }
    const rect = domElement.getBoundingClientRect();
    const {
      width,
      height,
      $
    } = getCssDimensions(domElement);
    let x = ($ ? round(rect.width) : rect.width) / width;
    let y = ($ ? round(rect.height) : rect.height) / height;

    // 0, NaN, or Infinity should always fallback to 1.

    if (!x || !Number.isFinite(x)) {
      x = 1;
    }
    if (!y || !Number.isFinite(y)) {
      y = 1;
    }
    return {
      x,
      y
    };
  }

  const noOffsets = /*#__PURE__*/createCoords(0);
  function getVisualOffsets(element) {
    const win = getWindow(element);
    if (!isWebKit() || !win.visualViewport) {
      return noOffsets;
    }
    return {
      x: win.visualViewport.offsetLeft,
      y: win.visualViewport.offsetTop
    };
  }
  function shouldAddVisualOffsets(element, isFixed, floatingOffsetParent) {
    if (isFixed === void 0) {
      isFixed = false;
    }
    if (!floatingOffsetParent || isFixed && floatingOffsetParent !== getWindow(element)) {
      return false;
    }
    return isFixed;
  }

  function getBoundingClientRect(element, includeScale, isFixedStrategy, offsetParent) {
    if (includeScale === void 0) {
      includeScale = false;
    }
    if (isFixedStrategy === void 0) {
      isFixedStrategy = false;
    }
    const clientRect = element.getBoundingClientRect();
    const domElement = unwrapElement(element);
    let scale = createCoords(1);
    if (includeScale) {
      if (offsetParent) {
        if (isElement(offsetParent)) {
          scale = getScale(offsetParent);
        }
      } else {
        scale = getScale(element);
      }
    }
    const visualOffsets = shouldAddVisualOffsets(domElement, isFixedStrategy, offsetParent) ? getVisualOffsets(domElement) : createCoords(0);
    let x = (clientRect.left + visualOffsets.x) / scale.x;
    let y = (clientRect.top + visualOffsets.y) / scale.y;
    let width = clientRect.width / scale.x;
    let height = clientRect.height / scale.y;
    if (domElement) {
      const win = getWindow(domElement);
      const offsetWin = offsetParent && isElement(offsetParent) ? getWindow(offsetParent) : offsetParent;
      let currentWin = win;
      let currentIFrame = getFrameElement(currentWin);
      while (currentIFrame && offsetParent && offsetWin !== currentWin) {
        const iframeScale = getScale(currentIFrame);
        const iframeRect = currentIFrame.getBoundingClientRect();
        const css = getComputedStyle$1(currentIFrame);
        const left = iframeRect.left + (currentIFrame.clientLeft + parseFloat(css.paddingLeft)) * iframeScale.x;
        const top = iframeRect.top + (currentIFrame.clientTop + parseFloat(css.paddingTop)) * iframeScale.y;
        x *= iframeScale.x;
        y *= iframeScale.y;
        width *= iframeScale.x;
        height *= iframeScale.y;
        x += left;
        y += top;
        currentWin = getWindow(currentIFrame);
        currentIFrame = getFrameElement(currentWin);
      }
    }
    return core.rectToClientRect({
      width,
      height,
      x,
      y
    });
  }

  // If <html> has a CSS width greater than the viewport, then this will be
  // incorrect for RTL.
  function getWindowScrollBarX(element, rect) {
    const leftScroll = getNodeScroll(element).scrollLeft;
    if (!rect) {
      return getBoundingClientRect(getDocumentElement(element)).left + leftScroll;
    }
    return rect.left + leftScroll;
  }

  function getHTMLOffset(documentElement, scroll) {
    const htmlRect = documentElement.getBoundingClientRect();
    const x = htmlRect.left + scroll.scrollLeft - getWindowScrollBarX(documentElement, htmlRect);
    const y = htmlRect.top + scroll.scrollTop;
    return {
      x,
      y
    };
  }

  function convertOffsetParentRelativeRectToViewportRelativeRect(_ref) {
    let {
      elements,
      rect,
      offsetParent,
      strategy
    } = _ref;
    const isFixed = strategy === 'fixed';
    const documentElement = getDocumentElement(offsetParent);
    const topLayer = elements ? isTopLayer(elements.floating) : false;
    if (offsetParent === documentElement || topLayer && isFixed) {
      return rect;
    }
    let scroll = {
      scrollLeft: 0,
      scrollTop: 0
    };
    let scale = createCoords(1);
    const offsets = createCoords(0);
    const isOffsetParentAnElement = isHTMLElement(offsetParent);
    if (isOffsetParentAnElement || !isOffsetParentAnElement && !isFixed) {
      if (getNodeName(offsetParent) !== 'body' || isOverflowElement(documentElement)) {
        scroll = getNodeScroll(offsetParent);
      }
      if (isOffsetParentAnElement) {
        const offsetRect = getBoundingClientRect(offsetParent);
        scale = getScale(offsetParent);
        offsets.x = offsetRect.x + offsetParent.clientLeft;
        offsets.y = offsetRect.y + offsetParent.clientTop;
      }
    }
    const htmlOffset = documentElement && !isOffsetParentAnElement && !isFixed ? getHTMLOffset(documentElement, scroll) : createCoords(0);
    return {
      width: rect.width * scale.x,
      height: rect.height * scale.y,
      x: rect.x * scale.x - scroll.scrollLeft * scale.x + offsets.x + htmlOffset.x,
      y: rect.y * scale.y - scroll.scrollTop * scale.y + offsets.y + htmlOffset.y
    };
  }

  function getClientRects(element) {
    return Array.from(element.getClientRects());
  }

  // Gets the entire size of the scrollable document area, even extending outside
  // of the `<html>` and `<body>` rect bounds if horizontally scrollable.
  function getDocumentRect(element) {
    const html = getDocumentElement(element);
    const scroll = getNodeScroll(element);
    const body = element.ownerDocument.body;
    const width = max(html.scrollWidth, html.clientWidth, body.scrollWidth, body.clientWidth);
    const height = max(html.scrollHeight, html.clientHeight, body.scrollHeight, body.clientHeight);
    let x = -scroll.scrollLeft + getWindowScrollBarX(element);
    const y = -scroll.scrollTop;
    if (getComputedStyle$1(body).direction === 'rtl') {
      x += max(html.clientWidth, body.clientWidth) - width;
    }
    return {
      width,
      height,
      x,
      y
    };
  }

  // Safety check: ensure the scrollbar space is reasonable in case this
  // calculation is affected by unusual styles.
  // Most scrollbars leave 15-18px of space.
  const SCROLLBAR_MAX = 25;
  function getViewportRect(element, strategy) {
    const win = getWindow(element);
    const html = getDocumentElement(element);
    const visualViewport = win.visualViewport;
    let width = html.clientWidth;
    let height = html.clientHeight;
    let x = 0;
    let y = 0;
    if (visualViewport) {
      width = visualViewport.width;
      height = visualViewport.height;
      const visualViewportBased = isWebKit();
      if (!visualViewportBased || visualViewportBased && strategy === 'fixed') {
        x = visualViewport.offsetLeft;
        y = visualViewport.offsetTop;
      }
    }
    const windowScrollbarX = getWindowScrollBarX(html);
    // <html> `overflow: hidden` + `scrollbar-gutter: stable` reduces the
    // visual width of the <html> but this is not considered in the size
    // of `html.clientWidth`.
    if (windowScrollbarX <= 0) {
      const doc = html.ownerDocument;
      const body = doc.body;
      const bodyStyles = getComputedStyle(body);
      const bodyMarginInline = doc.compatMode === 'CSS1Compat' ? parseFloat(bodyStyles.marginLeft) + parseFloat(bodyStyles.marginRight) || 0 : 0;
      const clippingStableScrollbarWidth = Math.abs(html.clientWidth - body.clientWidth - bodyMarginInline);
      if (clippingStableScrollbarWidth <= SCROLLBAR_MAX) {
        width -= clippingStableScrollbarWidth;
      }
    } else if (windowScrollbarX <= SCROLLBAR_MAX) {
      // If the <body> scrollbar is on the left, the width needs to be extended
      // by the scrollbar amount so there isn't extra space on the right.
      width += windowScrollbarX;
    }
    return {
      width,
      height,
      x,
      y
    };
  }

  // Returns the inner client rect, subtracting scrollbars if present.
  function getInnerBoundingClientRect(element, strategy) {
    const clientRect = getBoundingClientRect(element, true, strategy === 'fixed');
    const top = clientRect.top + element.clientTop;
    const left = clientRect.left + element.clientLeft;
    const scale = isHTMLElement(element) ? getScale(element) : createCoords(1);
    const width = element.clientWidth * scale.x;
    const height = element.clientHeight * scale.y;
    const x = left * scale.x;
    const y = top * scale.y;
    return {
      width,
      height,
      x,
      y
    };
  }
  function getClientRectFromClippingAncestor(element, clippingAncestor, strategy) {
    let rect;
    if (clippingAncestor === 'viewport') {
      rect = getViewportRect(element, strategy);
    } else if (clippingAncestor === 'document') {
      rect = getDocumentRect(getDocumentElement(element));
    } else if (isElement(clippingAncestor)) {
      rect = getInnerBoundingClientRect(clippingAncestor, strategy);
    } else {
      const visualOffsets = getVisualOffsets(element);
      rect = {
        x: clippingAncestor.x - visualOffsets.x,
        y: clippingAncestor.y - visualOffsets.y,
        width: clippingAncestor.width,
        height: clippingAncestor.height
      };
    }
    return core.rectToClientRect(rect);
  }
  function hasFixedPositionAncestor(element, stopNode) {
    const parentNode = getParentNode(element);
    if (parentNode === stopNode || !isElement(parentNode) || isLastTraversableNode(parentNode)) {
      return false;
    }
    return getComputedStyle$1(parentNode).position === 'fixed' || hasFixedPositionAncestor(parentNode, stopNode);
  }

  // A "clipping ancestor" is an `overflow` element with the characteristic of
  // clipping (or hiding) child elements. This returns all clipping ancestors
  // of the given element up the tree.
  function getClippingElementAncestors(element, cache) {
    const cachedResult = cache.get(element);
    if (cachedResult) {
      return cachedResult;
    }
    let result = getOverflowAncestors(element, [], false).filter(el => isElement(el) && getNodeName(el) !== 'body');
    let currentContainingBlockComputedStyle = null;
    const elementIsFixed = getComputedStyle$1(element).position === 'fixed';
    let currentNode = elementIsFixed ? getParentNode(element) : element;

    // https://developer.mozilla.org/en-US/docs/Web/CSS/Containing_block#identifying_the_containing_block
    while (isElement(currentNode) && !isLastTraversableNode(currentNode)) {
      const computedStyle = getComputedStyle$1(currentNode);
      const currentNodeIsContaining = isContainingBlock(currentNode);
      if (!currentNodeIsContaining && computedStyle.position === 'fixed') {
        currentContainingBlockComputedStyle = null;
      }
      const shouldDropCurrentNode = elementIsFixed ? !currentNodeIsContaining && !currentContainingBlockComputedStyle : !currentNodeIsContaining && computedStyle.position === 'static' && !!currentContainingBlockComputedStyle && (currentContainingBlockComputedStyle.position === 'absolute' || currentContainingBlockComputedStyle.position === 'fixed') || isOverflowElement(currentNode) && !currentNodeIsContaining && hasFixedPositionAncestor(element, currentNode);
      if (shouldDropCurrentNode) {
        // Drop non-containing blocks.
        result = result.filter(ancestor => ancestor !== currentNode);
      } else {
        // Record last containing block for next iteration.
        currentContainingBlockComputedStyle = computedStyle;
      }
      currentNode = getParentNode(currentNode);
    }
    cache.set(element, result);
    return result;
  }

  // Gets the maximum area that the element is visible in due to any number of
  // clipping ancestors.
  function getClippingRect(_ref) {
    let {
      element,
      boundary,
      rootBoundary,
      strategy
    } = _ref;
    const elementClippingAncestors = boundary === 'clippingAncestors' ? isTopLayer(element) ? [] : getClippingElementAncestors(element, this._c) : [].concat(boundary);
    const clippingAncestors = [...elementClippingAncestors, rootBoundary];
    const firstRect = getClientRectFromClippingAncestor(element, clippingAncestors[0], strategy);
    let top = firstRect.top;
    let right = firstRect.right;
    let bottom = firstRect.bottom;
    let left = firstRect.left;
    for (let i = 1; i < clippingAncestors.length; i++) {
      const rect = getClientRectFromClippingAncestor(element, clippingAncestors[i], strategy);
      top = max(rect.top, top);
      right = min(rect.right, right);
      bottom = min(rect.bottom, bottom);
      left = max(rect.left, left);
    }
    return {
      width: right - left,
      height: bottom - top,
      x: left,
      y: top
    };
  }

  function getDimensions(element) {
    const {
      width,
      height
    } = getCssDimensions(element);
    return {
      width,
      height
    };
  }

  function getRectRelativeToOffsetParent(element, offsetParent, strategy) {
    const isOffsetParentAnElement = isHTMLElement(offsetParent);
    const documentElement = getDocumentElement(offsetParent);
    const isFixed = strategy === 'fixed';
    const rect = getBoundingClientRect(element, true, isFixed, offsetParent);
    let scroll = {
      scrollLeft: 0,
      scrollTop: 0
    };
    const offsets = createCoords(0);

    // If the <body> scrollbar appears on the left (e.g. RTL systems). Use
    // Firefox with layout.scrollbar.side = 3 in about:config to test this.
    function setLeftRTLScrollbarOffset() {
      offsets.x = getWindowScrollBarX(documentElement);
    }
    if (isOffsetParentAnElement || !isOffsetParentAnElement && !isFixed) {
      if (getNodeName(offsetParent) !== 'body' || isOverflowElement(documentElement)) {
        scroll = getNodeScroll(offsetParent);
      }
      if (isOffsetParentAnElement) {
        const offsetRect = getBoundingClientRect(offsetParent, true, isFixed, offsetParent);
        offsets.x = offsetRect.x + offsetParent.clientLeft;
        offsets.y = offsetRect.y + offsetParent.clientTop;
      } else if (documentElement) {
        setLeftRTLScrollbarOffset();
      }
    }
    if (isFixed && !isOffsetParentAnElement && documentElement) {
      setLeftRTLScrollbarOffset();
    }
    const htmlOffset = documentElement && !isOffsetParentAnElement && !isFixed ? getHTMLOffset(documentElement, scroll) : createCoords(0);
    const x = rect.left + scroll.scrollLeft - offsets.x - htmlOffset.x;
    const y = rect.top + scroll.scrollTop - offsets.y - htmlOffset.y;
    return {
      x,
      y,
      width: rect.width,
      height: rect.height
    };
  }

  function isStaticPositioned(element) {
    return getComputedStyle$1(element).position === 'static';
  }

  function getTrueOffsetParent(element, polyfill) {
    if (!isHTMLElement(element) || getComputedStyle$1(element).position === 'fixed') {
      return null;
    }
    if (polyfill) {
      return polyfill(element);
    }
    let rawOffsetParent = element.offsetParent;

    // Firefox returns the <html> element as the offsetParent if it's non-static,
    // while Chrome and Safari return the <body> element. The <body> element must
    // be used to perform the correct calculations even if the <html> element is
    // non-static.
    if (getDocumentElement(element) === rawOffsetParent) {
      rawOffsetParent = rawOffsetParent.ownerDocument.body;
    }
    return rawOffsetParent;
  }

  // Gets the closest ancestor positioned element. Handles some edge cases,
  // such as table ancestors and cross browser bugs.
  function getOffsetParent(element, polyfill) {
    const win = getWindow(element);
    if (isTopLayer(element)) {
      return win;
    }
    if (!isHTMLElement(element)) {
      let svgOffsetParent = getParentNode(element);
      while (svgOffsetParent && !isLastTraversableNode(svgOffsetParent)) {
        if (isElement(svgOffsetParent) && !isStaticPositioned(svgOffsetParent)) {
          return svgOffsetParent;
        }
        svgOffsetParent = getParentNode(svgOffsetParent);
      }
      return win;
    }
    let offsetParent = getTrueOffsetParent(element, polyfill);
    while (offsetParent && isTableElement(offsetParent) && isStaticPositioned(offsetParent)) {
      offsetParent = getTrueOffsetParent(offsetParent, polyfill);
    }
    if (offsetParent && isLastTraversableNode(offsetParent) && isStaticPositioned(offsetParent) && !isContainingBlock(offsetParent)) {
      return win;
    }
    return offsetParent || getContainingBlock(element) || win;
  }

  const getElementRects = async function (data) {
    const getOffsetParentFn = this.getOffsetParent || getOffsetParent;
    const getDimensionsFn = this.getDimensions;
    const floatingDimensions = await getDimensionsFn(data.floating);
    return {
      reference: getRectRelativeToOffsetParent(data.reference, await getOffsetParentFn(data.floating), data.strategy),
      floating: {
        x: 0,
        y: 0,
        width: floatingDimensions.width,
        height: floatingDimensions.height
      }
    };
  };

  function isRTL(element) {
    return getComputedStyle$1(element).direction === 'rtl';
  }

  const platform = {
    convertOffsetParentRelativeRectToViewportRelativeRect,
    getDocumentElement,
    getClippingRect,
    getOffsetParent,
    getElementRects,
    getClientRects,
    getDimensions,
    getScale,
    isElement,
    isRTL
  };

  function rectsAreEqual(a, b) {
    return a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height;
  }

  // https://samthor.au/2021/observing-dom/
  function observeMove(element, onMove) {
    let io = null;
    let timeoutId;
    const root = getDocumentElement(element);
    function cleanup() {
      var _io;
      clearTimeout(timeoutId);
      (_io = io) == null || _io.disconnect();
      io = null;
    }
    function refresh(skip, threshold) {
      if (skip === void 0) {
        skip = false;
      }
      if (threshold === void 0) {
        threshold = 1;
      }
      cleanup();
      const elementRectForRootMargin = element.getBoundingClientRect();
      const {
        left,
        top,
        width,
        height
      } = elementRectForRootMargin;
      if (!skip) {
        onMove();
      }
      if (!width || !height) {
        return;
      }
      const insetTop = floor(top);
      const insetRight = floor(root.clientWidth - (left + width));
      const insetBottom = floor(root.clientHeight - (top + height));
      const insetLeft = floor(left);
      const rootMargin = -insetTop + "px " + -insetRight + "px " + -insetBottom + "px " + -insetLeft + "px";
      const options = {
        rootMargin,
        threshold: max(0, min(1, threshold)) || 1
      };
      let isFirstUpdate = true;
      function handleObserve(entries) {
        const ratio = entries[0].intersectionRatio;
        if (ratio !== threshold) {
          if (!isFirstUpdate) {
            return refresh();
          }
          if (!ratio) {
            // If the reference is clipped, the ratio is 0. Throttle the refresh
            // to prevent an infinite loop of updates.
            timeoutId = setTimeout(() => {
              refresh(false, 1e-7);
            }, 1000);
          } else {
            refresh(false, ratio);
          }
        }
        if (ratio === 1 && !rectsAreEqual(elementRectForRootMargin, element.getBoundingClientRect())) {
          // It's possible that even though the ratio is reported as 1, the
          // element is not actually fully within the IntersectionObserver's root
          // area anymore. This can happen under performance constraints. This may
          // be a bug in the browser's IntersectionObserver implementation. To
          // work around this, we compare the element's bounding rect now with
          // what it was at the time we created the IntersectionObserver. If they
          // are not equal then the element moved, so we refresh.
          refresh();
        }
        isFirstUpdate = false;
      }

      // Older browsers don't support a `document` as the root and will throw an
      // error.
      try {
        io = new IntersectionObserver(handleObserve, {
          ...options,
          // Handle <iframe>s
          root: root.ownerDocument
        });
      } catch (_e) {
        io = new IntersectionObserver(handleObserve, options);
      }
      io.observe(element);
    }
    refresh(true);
    return cleanup;
  }

  /**
   * Automatically updates the position of the floating element when necessary.
   * Should only be called when the floating element is mounted on the DOM or
   * visible on the screen.
   * @returns cleanup function that should be invoked when the floating element is
   * removed from the DOM or hidden from the screen.
   * @see https://floating-ui.com/docs/autoUpdate
   */
  function autoUpdate(reference, floating, update, options) {
    if (options === void 0) {
      options = {};
    }
    const {
      ancestorScroll = true,
      ancestorResize = true,
      elementResize = typeof ResizeObserver === 'function',
      layoutShift = typeof IntersectionObserver === 'function',
      animationFrame = false
    } = options;
    const referenceEl = unwrapElement(reference);
    const ancestors = ancestorScroll || ancestorResize ? [...(referenceEl ? getOverflowAncestors(referenceEl) : []), ...(floating ? getOverflowAncestors(floating) : [])] : [];
    ancestors.forEach(ancestor => {
      ancestorScroll && ancestor.addEventListener('scroll', update, {
        passive: true
      });
      ancestorResize && ancestor.addEventListener('resize', update);
    });
    const cleanupIo = referenceEl && layoutShift ? observeMove(referenceEl, update) : null;
    let reobserveFrame = -1;
    let resizeObserver = null;
    if (elementResize) {
      resizeObserver = new ResizeObserver(_ref => {
        let [firstEntry] = _ref;
        if (firstEntry && firstEntry.target === referenceEl && resizeObserver && floating) {
          // Prevent update loops when using the `size` middleware.
          // https://github.com/floating-ui/floating-ui/issues/1740
          resizeObserver.unobserve(floating);
          cancelAnimationFrame(reobserveFrame);
          reobserveFrame = requestAnimationFrame(() => {
            var _resizeObserver;
            (_resizeObserver = resizeObserver) == null || _resizeObserver.observe(floating);
          });
        }
        update();
      });
      if (referenceEl && !animationFrame) {
        resizeObserver.observe(referenceEl);
      }
      if (floating) {
        resizeObserver.observe(floating);
      }
    }
    let frameId;
    let prevRefRect = animationFrame ? getBoundingClientRect(reference) : null;
    if (animationFrame) {
      frameLoop();
    }
    function frameLoop() {
      const nextRefRect = getBoundingClientRect(reference);
      if (prevRefRect && !rectsAreEqual(prevRefRect, nextRefRect)) {
        update();
      }
      prevRefRect = nextRefRect;
      frameId = requestAnimationFrame(frameLoop);
    }
    update();
    return () => {
      var _resizeObserver2;
      ancestors.forEach(ancestor => {
        ancestorScroll && ancestor.removeEventListener('scroll', update);
        ancestorResize && ancestor.removeEventListener('resize', update);
      });
      cleanupIo == null || cleanupIo();
      (_resizeObserver2 = resizeObserver) == null || _resizeObserver2.disconnect();
      resizeObserver = null;
      if (animationFrame) {
        cancelAnimationFrame(frameId);
      }
    };
  }

  /**
   * Resolves with an object of overflow side offsets that determine how much the
   * element is overflowing a given clipping boundary on each side.
   * - positive = overflowing the boundary by that number of pixels
   * - negative = how many pixels left before it will overflow
   * - 0 = lies flush with the boundary
   * @see https://floating-ui.com/docs/detectOverflow
   */
  const detectOverflow = core.detectOverflow;

  /**
   * Modifies the placement by translating the floating element along the
   * specified axes.
   * A number (shorthand for `mainAxis` or distance), or an axes configuration
   * object may be passed.
   * @see https://floating-ui.com/docs/offset
   */
  const offset = core.offset;

  /**
   * Optimizes the visibility of the floating element by choosing the placement
   * that has the most space available automatically, without needing to specify a
   * preferred placement. Alternative to `flip`.
   * @see https://floating-ui.com/docs/autoPlacement
   */
  const autoPlacement = core.autoPlacement;

  /**
   * Optimizes the visibility of the floating element by shifting it in order to
   * keep it in view when it will overflow the clipping boundary.
   * @see https://floating-ui.com/docs/shift
   */
  const shift = core.shift;

  /**
   * Optimizes the visibility of the floating element by flipping the `placement`
   * in order to keep it in view when the preferred placement(s) will overflow the
   * clipping boundary. Alternative to `autoPlacement`.
   * @see https://floating-ui.com/docs/flip
   */
  const flip = core.flip;

  /**
   * Provides data that allows you to change the size of the floating element —
   * for instance, prevent it from overflowing the clipping boundary or match the
   * width of the reference element.
   * @see https://floating-ui.com/docs/size
   */
  const size = core.size;

  /**
   * Provides data to hide the floating element in applicable situations, such as
   * when it is not in the same clipping context as the reference element.
   * @see https://floating-ui.com/docs/hide
   */
  const hide = core.hide;

  /**
   * Provides data to position an inner element of the floating element so that it
   * appears centered to the reference element.
   * @see https://floating-ui.com/docs/arrow
   */
  const arrow = core.arrow;

  /**
   * Provides improved positioning for inline reference elements that can span
   * over multiple lines, such as hyperlinks or range selections.
   * @see https://floating-ui.com/docs/inline
   */
  const inline = core.inline;

  /**
   * Built-in `limiter` that will stop `shift()` at a certain point.
   */
  const limitShift = core.limitShift;

  /**
   * Computes the `x` and `y` coordinates that will place the floating element
   * next to a given reference element.
   */
  const computePosition = (reference, floating, options) => {
    // This caches the expensive `getClippingElementAncestors` function so that
    // multiple lifecycle resets re-use the same result. It only lives for a
    // single call. If other functions become expensive, we can add them as well.
    const cache = new Map();
    const mergedOptions = {
      platform,
      ...options
    };
    const platformWithCache = {
      ...mergedOptions.platform,
      _c: cache
    };
    return core.computePosition(reference, floating, {
      ...mergedOptions,
      platform: platformWithCache
    });
  };

  exports.arrow = arrow;
  exports.autoPlacement = autoPlacement;
  exports.autoUpdate = autoUpdate;
  exports.computePosition = computePosition;
  exports.detectOverflow = detectOverflow;
  exports.flip = flip;
  exports.getOverflowAncestors = getOverflowAncestors;
  exports.hide = hide;
  exports.inline = inline;
  exports.limitShift = limitShift;
  exports.offset = offset;
  exports.platform = platform;
  exports.shift = shift;
  exports.size = size;

  // Set here as well, so the component scripts find it on pages with an AMD
  // loader.
  window.FloatingUIDOM = exports;

}));

// components/sidebar/sidebar.js
(function () {
  "use strict";

  const SIDEBAR_COOKIE_NAME = "sidebar_state";
  const SIDEBAR_COOKIE_MAX_AGE = 60 * 60 * 24 * 7; // 7 days
  const SIDEBAR_KEYBOARD_SHORTCUT = "b";
  const MOBILE_QUERY = "(max-width: 767px)";

  // shadcn has one SidebarProvider context; the port marker names each
  // sidebar so several can live on a page.
  const WRAPPER = "[data-templ-sidebar-id]";

  function wrapperFor(sidebarId) {
    return document.querySelector('[data-templ-sidebar-id="' + sidebarId + '"]');
  }

  // SidebarProvider.openMobile survives the Sheet's viewport-driven unmount.
  function openMobileOf(sidebarId) {
    return !!anyWrapper(sidebarId)?._templOpenMobile;
  }

  // SidebarProvider.setOpenMobile: state is independent of the mounted Sheet.
  function setOpenMobile(open, sidebarId) {
    const wrapper = anyWrapper(sidebarId);
    if (!wrapper) return;
    wrapper._templOpenMobile = !!open;
    if (!window.matchMedia(MOBILE_QUERY).matches) return;
    const popup = document.getElementById(wrapper.getAttribute("data-templ-sidebar-id") + "-mobile");
    const dialog = window.templ?.dialog;
    if (!popup || !dialog) return;
    if (open && !dialog.isOpen(popup)) dialog.open(popup);
    else if (!open && dialog.isOpen(popup)) dialog.close(popup);
  }

  // shadcn's Sidebar renders its children in the mobile sheet below md and
  // in sidebar-inner otherwise: they render once and move between the two.
  function place(sidebar) {
    const sidebarId = sidebar.getAttribute("data-templ-sidebar-id");
    const inner = sidebar.querySelector('[data-slot="sidebar-inner"]');
    const portal = document.querySelector('[data-templ-sidebar-mobile-portal="' + sidebarId + '"]');
    if (!inner || !portal) return;

    const isMobile = window.matchMedia(MOBILE_QUERY).matches;
    const from = isMobile ? inner : portal;
    const to = isMobile ? portal : inner;
    if (from.firstChild) to.append(...from.childNodes);
    syncTooltips(sidebarId);

    // Mount/unmount the Sheet with open={openMobile}, as in shadcn's Sidebar.
    const popup = document.getElementById(sidebarId + "-mobile");
    const dialog = window.templ?.dialog;
    if (!popup || !dialog) return;
    if (isMobile && openMobileOf(sidebarId) && !dialog.isOpen(popup)) {
      dialog.open(popup);
    } else if (!isMobile && dialog.isOpen(popup)) {
      dialog.close(popup);
    }
  }

  window.templ.lifecycle.register(WRAPPER, { init: place });
  window.addEventListener("resize", () => document.querySelectorAll(WRAPPER).forEach(place));

  // SidebarMenuButton's TooltipContent: hidden={state !== "collapsed" ||
  // isMobile}, unless the tooltip prop sets hidden itself.
  function syncTooltips(sidebarId) {
    const wrapper = wrapperFor(sidebarId);
    const portal = document.querySelector('[data-templ-sidebar-mobile-portal="' + sidebarId + '"]');
    const hidden = wrapper?.getAttribute("data-state") !== "collapsed" || window.matchMedia(MOBILE_QUERY).matches;
    [wrapper, portal].forEach((root) => {
      root?.querySelectorAll("[data-templ-tooltip-trigger]").forEach((trigger) => {
        const popup = document.getElementById(trigger.getAttribute("data-templ-tooltip-trigger"));
        if (!popup || popup.hasAttribute("data-templ-tooltip-hidden")) return;
        popup.hidden = hidden;
      });
    });
  }

  function toggleSidebar(sidebarId) {
    // shadcn's toggleSidebar: setOpenMobile((open) => !open) below md.
    if (window.matchMedia(MOBILE_QUERY).matches) {
      setOpenMobile(!openMobileOf(sidebarId), sidebarId);
      return;
    }

    const wrapper = wrapperFor(sidebarId);
    if (!wrapper) return;
    const mode = wrapper.getAttribute("data-templ-collapsible");
    if (mode === "none") return;

    const collapsed = wrapper.getAttribute("data-state") !== "collapsed";
    wrapper.setAttribute("data-state", collapsed ? "collapsed" : "expanded");
    // Like shadcn, data-collapsible carries the mode only while collapsed,
    // so icon/offcanvas selectors need no extra state check.
    wrapper.setAttribute("data-collapsible", collapsed ? mode : "");

    syncTooltips(sidebarId);

    document.cookie =
      SIDEBAR_COOKIE_NAME +
      "=" +
      (collapsed ? "false" : "true") +
      "; path=/; max-age=" +
      SIDEBAR_COOKIE_MAX_AGE;
  }

  document.addEventListener("click", (e) => {
    if (!(e.target instanceof Element)) return;
    // SidebarTrigger and SidebarRail; the port marker names their sidebar.
    const trigger = e.target.closest("[data-templ-sidebar-trigger]");
    if (!trigger) return;
    const targetId = trigger.getAttribute("data-templ-sidebar-trigger");
    if (targetId) toggleSidebar(targetId);
  });

  // Sheet onOpenChange={setOpenMobile}; unmount closes do not change state.
  document.addEventListener("dialog-open-change", (event) => {
    if (!(event.target instanceof Element)) return;
    const id = event.target.id;
    if (!id.endsWith("-mobile")) return;
    const sidebarId = id.slice(0, -"-mobile".length);
    if (wrapperFor(sidebarId)) setOpenMobile(event.detail.open, sidebarId);
  });

  // The useSidebar pendant: the same seven members as the React hook,
  // addressing the first sidebar unless a sidebarId is given.
  function anyWrapper(sidebarId) {
    return sidebarId
      ? wrapperFor(sidebarId)
      : document.querySelector(WRAPPER);
  }

  window.templ = window.templ || {};
  window.templ.sidebar = {
    state(sidebarId) {
      return anyWrapper(sidebarId)?.getAttribute("data-state") || null;
    },
    open(sidebarId) {
      return this.state(sidebarId) === "expanded";
    },
    setOpen(open, sidebarId) {
      const wrapper = anyWrapper(sidebarId);
      if (!wrapper) return;
      if (this.open(sidebarId) !== open) {
        toggleSidebar(wrapper.getAttribute("data-templ-sidebar-id"));
      }
    },
    openMobile(sidebarId) {
      return openMobileOf(sidebarId);
    },
    setOpenMobile(open, sidebarId) {
      setOpenMobile(open, sidebarId);
    },
    isMobile() {
      return window.matchMedia(MOBILE_QUERY).matches;
    },
    // The subscription half of useSidebar().isMobile: calls fn with the
    // current value now and again whenever it changes, like a re-render.
    // fn returns false when its elements are gone (the unmount pendant),
    // which unsubscribes it.
    onMobileChange(fn) {
      const query = window.matchMedia(MOBILE_QUERY);
      const listener = () => {
        if (fn(query.matches) === false) query.removeEventListener("change", listener);
      };
      query.addEventListener("change", listener);
      listener();
    },
    toggleSidebar(sidebarId) {
      const wrapper = anyWrapper(sidebarId);
      if (wrapper) toggleSidebar(wrapper.getAttribute("data-templ-sidebar-id"));
    },
  };

  // Cmd/Ctrl + shortcut key toggles the sidebar.
  document.addEventListener("keydown", (e) => {
    if (!(e.ctrlKey || e.metaKey) || e.key.length !== 1) return;
    const wrapper = document.querySelector(WRAPPER);
    if (!wrapper || e.key.toLowerCase() !== SIDEBAR_KEYBOARD_SHORTCUT) return;
    e.preventDefault();
    toggleSidebar(wrapper.getAttribute("data-templ-sidebar-id"));
  });
})();

// components/switch/switch.js
(function () {
  "use strict";

  // Vanilla port of Base UI's switch: the root span behavior comes from
  // switch/root/SwitchRoot.tsx, the non-native button keyboard semantics from
  // internals/use-button/useButton.ts. Clicks, Enter and Space forward to the
  // visually hidden native checkbox beside the root; the input's change event
  // syncs the state attributes back onto the root and thumb.

  const ROOT = '[data-slot="switch"]';
  // Base UI renders the hidden input right beside the root, without markers.
  const INPUT = ROOT + ' + input[type="checkbox"]';

  function inputOf(root) {
    const next = root.nextElementSibling;
    return next && next.matches(INPUT) ? next : null;
  }

  function rootOf(input) {
    return input.matches && input.matches(INPUT) ? input.previousElementSibling : null;
  }

  function isDisabled(root, input) {
    return (input && input.disabled) || root.getAttribute("aria-disabled") === "true";
  }

  function isReadOnly(root) {
    return root.getAttribute("aria-readonly") === "true";
  }

  // Port of utils/dispatchClickWithModifiers.ts: the constructed click keeps
  // the source event's modifier state and still runs native activation
  // behavior (toggling the input).
  function forwardClick(target, sourceEvent) {
    target.dispatchEvent(
      new PointerEvent("click", {
        bubbles: true,
        cancelable: true,
        composed: true,
        detail: 0,
        shiftKey: sourceEvent.shiftKey,
        ctrlKey: sourceEvent.ctrlKey,
        altKey: sourceEvent.altKey,
        metaKey: sourceEvent.metaKey,
      }),
    );
  }

  function sync(root, input) {
    const checked = input.checked;
    root.setAttribute("aria-checked", String(checked));
    root.toggleAttribute("data-checked", checked);
    root.toggleAttribute("data-unchecked", !checked);
    // Base UI mirrors the state onto the thumb via the stateAttributesMapping.
    const thumb = root.querySelector('[data-slot="switch-thumb"]');
    if (thumb) {
      thumb.toggleAttribute("data-checked", checked);
      thumb.toggleAttribute("data-unchecked", !checked);
    }
  }

  function requestCheckedChange(root, input, sourceEvent) {
    const nextChecked = !input.checked;
    const change = new CustomEvent("switch-change", {
      bubbles: true,
      cancelable: true,
      detail: { checked: nextChecked },
    });
    root.dispatchEvent(change);
    if (change.defaultPrevented || root.hasAttribute("data-templ-checked")) return;
    forwardClick(input, sourceEvent);
  }

  // SwitchRoot onClick: cancel the click's default (a wrapping label would
  // otherwise forward it to the input a second time) and toggle through the
  // hidden input so the native change event fires.
  document.addEventListener("click", (e) => {
    const root = e.target.closest && e.target.closest(ROOT);
    if (!root) return;
    const input = inputOf(root);
    if (!input) return;
    if (isDisabled(root, input)) {
      // useButton prevents clicks on disabled non-native buttons.
      e.preventDefault();
      return;
    }
    if (isReadOnly(root)) return;
    e.preventDefault();
    requestCheckedChange(root, input, e);
  });

  document.addEventListener("change", (e) => {
    const input = e.target;
    const root = rootOf(input);
    if (root) sync(root, input);
  });

  document.addEventListener("keydown", (e) => {
    const root = e.target;
    if (!root.matches || !root.matches(ROOT)) return;
    if (isDisabled(root, inputOf(root))) return;
    if (e.key === "Enter") {
      // useButton: Enter activates non-native buttons on keydown.
      if (e.defaultPrevented) return;
      e.preventDefault();
      forwardClick(root, e);
    } else if (e.key === " ") {
      // useButton: Space activates on keyup; prevent the page scroll.
      e.preventDefault();
    }
  });

  // useButton keyup: Space dispatches the click on the root itself, which the
  // click handler above forwards to the input.
  document.addEventListener("keyup", (e) => {
    const root = e.target;
    if (!root.matches || !root.matches(ROOT)) return;
    if (e.key !== " " || e.defaultPrevented) return;
    if (isDisabled(root, inputOf(root))) return;
    forwardClick(root, e);
  });

  // Focus on the hidden input (label clicks, programmatic focus) belongs on
  // the root (SwitchRoot's input onFocus).
  document.addEventListener("focusin", (e) => {
    const root = rootOf(e.target);
    if (root) root.focus();
  });

  let labelId = 0;

  function setup(root) {
    const input = inputOf(root);
    if (!input) return;
    // The clicks dispatched on the hidden input are an implementation detail
    // and must not reach ancestors, which already receive the original click
    // (SwitchRoot's input onClick).
    input.addEventListener("click", (e) => e.stopPropagation());
    // useAriaLabelledBy fallback: the span control is labelled by the native
    // label associated with the hidden input.
    if (!root.hasAttribute("aria-labelledby") && !root.hasAttribute("aria-label")) {
      const label =
        input.parentElement && input.parentElement.tagName === "LABEL"
          ? input.parentElement
          : input.labels && input.labels[0];
      if (label) {
        if (!label.id) {
          labelId += 1;
          label.id = (input.id || "templ-switch-" + labelId) + "-label";
        }
        root.setAttribute("aria-labelledby", label.id);
      }
    }
    sync(root, input);
  }

  window.templ.lifecycle.register(ROOT, { init: setup });
})();

// components/toast/toast.js
// Port of @base-ui/react toast 1.6.0 (createToastManager, store, Provider,
// Viewport, Root, Content, Title, Description, Action, Close) with shadcn's
// Toaster and ToastList: the Toaster's templates render each toast.
(function () {
  "use strict";

  const VIEWPORT = '[data-slot="toast-viewport"]';

  const SWIPE_THRESHOLD = 40;
  const REVERSE_CANCEL_THRESHOLD = 10;
  const OPPOSITE_DIRECTION_DAMPING_FACTOR = 0.5;
  const MIN_DRAG_THRESHOLD = 1;
  const TOAST_SWIPE_IGNORE_SELECTOR = "[data-base-ui-swipe-ignore],[data-swipe-ignore]";
  // ToastRoot's default swipeDirection.
  const swipeDirections = ["down", "right"];

  let counter = 0;
  function generateId(prefix) {
    counter += 1;
    return `${prefix}-${Math.random().toString(36).slice(2, 6)}-${counter}`;
  }

  const isFocusVisible = (element) => {
    if (!element) return false;
    try {
      return element.matches(":focus-visible");
    } catch {
      return true;
    }
  };
  const contains = (parent, child) => !!parent && !!child && parent.contains(child);

  function createTimeout() {
    let id = 0;
    return {
      start(delay, fn) {
        clearTimeout(id);
        id = setTimeout(fn, delay);
      },
      clear() {
        clearTimeout(id);
      },
    };
  }

  // ----- createToastManager -------------------------------------------------

  function createToastManager() {
    const listeners = new Set();
    function emit(data) {
      listeners.forEach((listener) => listener(data));
    }
    return {
      " subscribe"(listener) {
        listeners.add(listener);
        return () => listeners.delete(listener);
      },
      add(options) {
        const id = options.id || generateId("toast");
        emit({ action: "add", options: { ...options, id, transitionStatus: "starting" } });
        return id;
      },
      close(id) {
        emit({ action: "close", options: { id } });
      },
      update(id, updates) {
        emit({ action: "update", options: { ...updates, id } });
      },
      promise(promiseValue, options) {
        let handledPromise = promiseValue;
        emit({
          action: "promise",
          options: {
            ...options,
            promise: promiseValue,
            setPromise(promise) {
              handledPromise = promise;
            },
          },
        });
        return handledPromise;
      },
    };
  }

  function resolvePromiseOptions(options, result) {
    if (typeof options === "string") return { description: options };
    if (typeof options === "function") {
      const resolvedOptions = options(result);
      return typeof resolvedOptions === "string" ? { description: resolvedOptions } : resolvedOptions;
    }
    return options;
  }

  // ----- store --------------------------------------------------------------

  function createToastMetadata(toasts) {
    const metadata = new Map();
    let visibleIndex = 0;
    let offsetY = 0;
    toasts.forEach((toast, toastIndex) => {
      const isEnding = toast.transitionStatus === "ending";
      metadata.set(toast.id, { value: toast, domIndex: toastIndex, visibleIndex: isEnding ? -1 : visibleIndex, offsetY });
      offsetY += toast.height || 0;
      if (!isEnding) visibleIndex += 1;
    });
    return metadata;
  }

  // Marks the active (non-ending) toasts beyond limit as limited, newest
  // first.
  function applyLimited(toasts, limit) {
    let activeIndex = 0;
    return toasts.map((toast) => {
      if (toast.transitionStatus === "ending") return toast;
      const limited = activeIndex >= limit;
      activeIndex += 1;
      return toast.limited === limited ? toast : { ...toast, limited };
    });
  }

  const selectors = {
    toast: (state, id) => state.toastMetadata.get(id)?.value,
    toastIndex: (state, id) => state.toastMetadata.get(id)?.domIndex ?? -1,
    toastOffsetY: (state, id) => state.toastMetadata.get(id)?.offsetY ?? 0,
    toastVisibleIndex: (state, id) => state.toastMetadata.get(id)?.visibleIndex ?? -1,
    isEmpty: (state) => state.toasts.length === 0,
    expanded: (state) => state.hovering || state.focused,
    expandedOrOutOfFocus: (state) => state.hovering || state.focused || !state.isWindowFocused,
  };

  class ToastStore {
    timers = new Map();
    areTimersPaused = false;

    constructor(initialState, onChange) {
      this.state = { ...initialState, toastMetadata: createToastMetadata(initialState.toasts) };
      this.onChange = onChange;
    }

    set(key, value) {
      this.update({ [key]: value });
    }

    update(updates) {
      this.state = { ...this.state, ...updates };
      this.onChange();
    }

    setFocused(focused) {
      this.set("focused", focused);
    }

    setHovering(hovering) {
      this.set("hovering", hovering);
    }

    setIsWindowFocused(isWindowFocused) {
      this.set("isWindowFocused", isWindowFocused);
    }

    setPrevFocusElement(prevFocusElement) {
      this.set("prevFocusElement", prevFocusElement);
    }

    removeToast(toastId, behavior = {}) {
      const index = selectors.toastIndex(this.state, toastId);
      if (index === -1) return;
      const toast = this.state.toasts[index];
      if (!behavior.skipOnRemove) toast?.onRemove?.();
      const newToasts = [...this.state.toasts];
      newToasts.splice(index, 1);
      this.setToasts(newToasts);
    }

    addToast = (toast) => {
      const { timeout, limit } = this.state;
      const id = toast.id || generateId("toast");
      if (toast.id) {
        const existingToast = selectors.toast(this.state, toast.id);
        if (existingToast) {
          if (existingToast.transitionStatus === "ending") {
            this.removeToast(toast.id, { skipOnRemove: true });
          } else {
            const { id: ignoredId, transitionStatus: ignoredTransitionStatus, ...updates } = toast;
            this.updateToastInternal(toast.id, updates, { resetTimer: true, markUpdated: true });
            return toast.id;
          }
        }
      }
      const toastToAdd = { ...toast, id, updateKey: 0, transitionStatus: "starting" };
      const updatedToasts = [toastToAdd, ...this.state.toasts];
      this.setToasts(applyLimited(updatedToasts, limit));
      const duration = toastToAdd.timeout ?? timeout;
      if (toastToAdd.type !== "loading" && duration > 0) {
        this.scheduleTimer(id, duration, () => this.closeToast(id));
      }
      if (selectors.expandedOrOutOfFocus(this.state)) this.pauseTimers();
      return id;
    };

    updateToast = (id, updates) => {
      this.updateToastInternal(id, updates, { markUpdated: true });
    };

    updateToastInternal = (id, updates, behavior = {}) => {
      const { timeout, toasts } = this.state;
      const prevToast = selectors.toast(this.state, id) ?? null;
      if (!prevToast) return;
      // Ignore updates for toasts that are already closing.
      if (prevToast.transitionStatus === "ending") return;
      const nextToast = {
        ...prevToast,
        ...updates,
        ...(behavior.markUpdated && { updateKey: (prevToast.updateKey ?? 0) + 1 }),
      };
      this.setToasts(toasts.map((toast) => (toast.id === id ? nextToast : toast)));
      const nextTimeout = nextToast.timeout ?? timeout;
      const prevTimeout = prevToast?.timeout ?? timeout;
      const timeoutUpdated = Object.hasOwn(updates, "timeout");
      const shouldHaveTimer = nextToast.transitionStatus !== "ending" && nextToast.type !== "loading" && nextTimeout > 0;
      const hasTimer = this.timers.has(id);
      const timeoutChanged = prevTimeout !== nextTimeout;
      const wasLoading = prevToast?.type === "loading";
      if (!shouldHaveTimer && hasTimer) {
        this.clearTimer(id);
        return;
      }
      if (shouldHaveTimer && (!hasTimer || timeoutChanged || timeoutUpdated || wasLoading || behavior.resetTimer)) {
        this.clearTimer(id);
        this.scheduleTimer(id, nextTimeout, () => this.closeToast(id));
        if (selectors.expandedOrOutOfFocus(this.state)) this.pauseTimers();
      }
    };

    closeToast = (toastId) => {
      const closeAll = toastId === undefined;
      const { limit, toasts } = this.state;
      let toastsToClose;
      if (closeAll) {
        toastsToClose = toasts;
        this.clearTimers();
      } else {
        const toast = selectors.toast(this.state, toastId);
        if (!toast) return;
        toastsToClose = [toast];
        this.clearTimer(toastId);
      }
      const endingToasts = toasts.map((item) =>
        closeAll || item.id === toastId ? { ...item, transitionStatus: "ending", height: 0 } : item,
      );
      const newToasts = applyLimited(endingToasts, limit);
      const updates = { toasts: newToasts, toastMetadata: createToastMetadata(newToasts) };
      const hasActiveToasts = newToasts.some((toast) => toast.transitionStatus !== "ending");
      if (!hasActiveToasts) {
        updates.hovering = false;
        updates.focused = false;
      }
      this.update(updates);
      toastsToClose.forEach((toast) => {
        if (toast.transitionStatus !== "ending") toast.onClose?.();
      });
      this.handleFocusManagement(toastId);
    };

    promiseToast = (promiseValue, options) => {
      // Create a loading toast (which does not auto-dismiss).
      const loadingOptions = resolvePromiseOptions(options.loading);
      const id = this.addToast({ ...loadingOptions, type: "loading" });
      const handledPromise = promiseValue
        .then((result) => {
          const successOptions = resolvePromiseOptions(options.success, result);
          this.updateToast(id, { ...successOptions, type: "success", timeout: successOptions.timeout });
          return result;
        })
        .catch((error) => {
          const errorOptions = resolvePromiseOptions(options.error, error);
          this.updateToast(id, { ...errorOptions, type: "error", timeout: errorOptions.timeout });
          return Promise.reject(error);
        });
      if ({}.hasOwnProperty.call(options, "setPromise")) options.setPromise(handledPromise);
      return handledPromise;
    };

    pauseTimers() {
      if (this.areTimersPaused) return;
      this.areTimersPaused = true;
      this.timers.forEach((timer) => {
        if (timer.timeout) {
          timer.timeout.clear();
          const elapsed = Date.now() - timer.start;
          const remaining = timer.delay - elapsed;
          timer.remaining = remaining > 0 ? remaining : 0;
        }
      });
    }

    resumeTimers() {
      if (!this.areTimersPaused) return;
      this.areTimersPaused = false;
      this.timers.forEach((timer, id) => {
        timer.remaining = timer.remaining > 0 ? timer.remaining : timer.delay;
        timer.timeout ??= createTimeout();
        timer.timeout.start(timer.remaining, () => {
          this.handleTimerFired(id);
          timer.callback();
        });
        timer.start = Date.now();
      });
    }

    restoreFocusToPrevElement() {
      this.state.prevFocusElement?.focus({ preventScroll: true });
    }

    handleDocumentPointerDown = (event) => {
      if (event.pointerType !== "touch") return;
      if (contains(this.state.viewport, event.target)) return;
      // Explicit touch activity outside the viewport ends the paused state.
      this.resumeTimers();
      this.update({ hovering: false, focused: false });
    };

    scheduleTimer(id, delay, callback) {
      const start = Date.now();
      const shouldStartActive = !selectors.expandedOrOutOfFocus(this.state);
      const currentTimeout = shouldStartActive ? createTimeout() : undefined;
      currentTimeout?.start(delay, () => {
        this.handleTimerFired(id);
        callback();
      });
      this.timers.set(id, { timeout: currentTimeout, start: shouldStartActive ? start : 0, delay, remaining: delay, callback });
    }

    clearTimers() {
      this.timers.forEach((timer) => timer.timeout?.clear());
      this.timers.clear();
      this.areTimersPaused = false;
    }

    clearTimer(id) {
      this.timers.get(id)?.timeout?.clear();
      this.timers.delete(id);
      this.resetPausedStateIfNoTimersRemain();
    }

    handleTimerFired(id) {
      this.timers.delete(id);
      this.resetPausedStateIfNoTimersRemain();
    }

    resetPausedStateIfNoTimersRemain() {
      if (this.timers.size === 0) this.areTimersPaused = false;
    }

    setToasts(newToasts) {
      const updates = { toasts: newToasts, toastMetadata: createToastMetadata(newToasts) };
      if (newToasts.length === 0) {
        updates.hovering = false;
        updates.focused = false;
      }
      this.update(updates);
    }

    handleFocusManagement(toastId) {
      const activeEl = document.activeElement;
      if (!this.state.viewport || !contains(this.state.viewport, activeEl) || !isFocusVisible(activeEl)) return;
      if (toastId === undefined) {
        this.restoreFocusToPrevElement();
        return;
      }
      const toasts = this.state.toasts;
      const currentIndex = selectors.toastIndex(this.state, toastId);
      let nextToast = null;
      // Try to find the next toast that isn't animating out
      let index = currentIndex + 1;
      while (index < toasts.length) {
        if (toasts[index].transitionStatus !== "ending") {
          nextToast = toasts[index];
          break;
        }
        index += 1;
      }
      // Go backwards if no next toast is found
      if (!nextToast) {
        index = currentIndex - 1;
        while (index >= 0) {
          if (toasts[index].transitionStatus !== "ending") {
            nextToast = toasts[index];
            break;
          }
          index -= 1;
        }
      }
      if (nextToast) nextToast.ref?.current?.focus();
      else this.restoreFocusToPrevElement();
    }
  }

  // ----- ToastRoot ----------------------------------------------------------

  function applyDirectionalDamping(deltaX, deltaY) {
    const damp = (d) => (d > 0 ? d ** OPPOSITE_DIRECTION_DAMPING_FACTOR : -(Math.abs(d) ** OPPOSITE_DIRECTION_DAMPING_FACTOR));
    let newDeltaX = deltaX;
    let newDeltaY = deltaY;
    if (!swipeDirections.includes("left") && !swipeDirections.includes("right")) {
      newDeltaX = damp(deltaX);
    } else {
      if (!swipeDirections.includes("right") && deltaX > 0) newDeltaX = damp(deltaX);
      if (!swipeDirections.includes("left") && deltaX < 0) newDeltaX = damp(deltaX);
    }
    if (!swipeDirections.includes("up") && !swipeDirections.includes("down")) {
      newDeltaY = damp(deltaY);
    } else {
      if (!swipeDirections.includes("down") && deltaY > 0) newDeltaY = damp(deltaY);
      if (!swipeDirections.includes("up") && deltaY < 0) newDeltaY = damp(deltaY);
    }
    return { x: newDeltaX, y: newDeltaY };
  }

  function getDisplacement(direction, deltaX, deltaY) {
    switch (direction) {
      case "up":
        return -deltaY;
      case "down":
        return deltaY;
      case "left":
        return -deltaX;
      case "right":
        return deltaX;
      default:
        return 0;
    }
  }

  function getElementTransform(element) {
    const transform = getComputedStyle(element).transform;
    let translateX = 0;
    let translateY = 0;
    let scale = 1;
    if (transform && transform !== "none") {
      const matrix = transform.match(/matrix(?:3d)?\(([^)]+)\)/);
      if (matrix) {
        const values = matrix[1].split(", ").map(parseFloat);
        if (values.length === 6) {
          translateX = values[4];
          translateY = values[5];
          scale = Math.sqrt(values[0] * values[0] + values[1] * values[1]);
        } else if (values.length === 16) {
          translateX = values[12];
          translateY = values[13];
          scale = values[0];
        }
      }
    }
    return { x: translateX, y: translateY, scale };
  }

  // Mounts one toast of ToastList from the Toaster's template. The element
  // keeps ToastRoot's React state.
  function mountToast(provider, toast) {
    const { store, portal } = provider;
    const el = portal.querySelector("template[data-templ-toast-list]").content.firstElementChild.cloneNode(true);
    const content = el.querySelector('[data-slot="toast-content"]');
    const title = el.querySelector('[data-slot="toast-title"]');
    const description = el.querySelector('[data-slot="toast-description"]');
    const action = el.querySelector('[data-slot="toast-action"]');
    const close = el.querySelector('[data-slot="toast-close"]');
    const r = {
      id: toast.id,
      el,
      content,
      title,
      description,
      action,
      close,
      titleText: title.parentElement,
      icon: null,
      iconType: undefined,
      titleId: undefined,
      descriptionId: undefined,
      ownTitleId: "base-ui-" + generateId("toast-title"),
      ownDescriptionId: "base-ui-" + generateId("toast-description"),
      closeHasFocus: false,
      isSwiping: false,
      isRealSwipe: false,
      currentSwipeDirection: undefined,
      dragDismissed: false,
      dragOffset: { x: 0, y: 0 },
      initialTransform: { x: 0, y: 0, scale: 1 },
      lockedDirection: null,
      dragStartPos: { x: 0, y: 0 },
      intendedSwipeDirection: undefined,
      maxSwipeDisplacement: 0,
      cancelledSwipe: false,
      swipeCancelBaseline: { x: 0, y: 0 },
      isFirstPointerMove: false,
      activePointerId: null,
      dragAbortController: null,
      ending: false,
    };
    title.remove();
    description.remove();
    action.remove();
    const toastOf = () => selectors.toast(store.state, r.id);

    // recalculateHeight
    r.recalculateHeight = () => {
      const previousHeight = el.style.height;
      el.style.height = "auto";
      const height = el.offsetHeight;
      el.style.height = previousHeight;
      const current = toastOf();
      if (!current) return;
      store.updateToastInternal(r.id, {
        ref: { current: el },
        height,
        ...(current.transitionStatus === "starting" ? { transitionStatus: undefined } : {}),
      });
    };

    const setDragOffset = (next) => {
      r.dragOffset = next;
    };

    const handleSwipeEnd = (event) => {
      if (event.pointerId !== r.activePointerId) return;
      r.activePointerId = null;
      r.dragAbortController?.abort();
      r.dragAbortController = null;
      r.isSwiping = false;
      r.isRealSwipe = false;
      r.lockedDirection = null;
      const resolvedInitialTransform = r.initialTransform;
      if (event.type === "pointercancel" || r.cancelledSwipe) {
        setDragOffset({ x: resolvedInitialTransform.x, y: resolvedInitialTransform.y });
        r.currentSwipeDirection = undefined;
        provider.render();
        return;
      }
      let shouldClose = false;
      const deltaX = r.dragOffset.x - resolvedInitialTransform.x;
      const deltaY = r.dragOffset.y - resolvedInitialTransform.y;
      let dismissDirection;
      for (const direction of swipeDirections) {
        if (direction === "right" && deltaX > SWIPE_THRESHOLD) dismissDirection = "right";
        if (direction === "left" && deltaX < -SWIPE_THRESHOLD) dismissDirection = "left";
        if (direction === "down" && deltaY > SWIPE_THRESHOLD) dismissDirection = "down";
        if (direction === "up" && deltaY < -SWIPE_THRESHOLD) dismissDirection = "up";
        if (dismissDirection) {
          shouldClose = true;
          break;
        }
      }
      if (shouldClose) {
        r.currentSwipeDirection = dismissDirection;
        r.dragDismissed = true;
        store.closeToast(r.id);
      } else {
        setDragOffset({ x: resolvedInitialTransform.x, y: resolvedInitialTransform.y });
        r.currentSwipeDirection = undefined;
        provider.render();
      }
    };
    r.handleSwipeEnd = handleSwipeEnd;

    el.addEventListener("pointerdown", (event) => {
      if (event.button !== 0) return;
      if (event.pointerType === "touch") store.pauseTimers();
      const target = event.target;
      const isInteractiveElement = target
        ? target.closest(`button,a,input,textarea,[role="button"],${TOAST_SWIPE_IGNORE_SELECTOR}`)
        : false;
      if (isInteractiveElement) return;
      r.cancelledSwipe = false;
      r.intendedSwipeDirection = undefined;
      r.maxSwipeDisplacement = 0;
      r.activePointerId = event.pointerId;
      r.dragStartPos = { x: event.clientX, y: event.clientY };
      r.swipeCancelBaseline = r.dragStartPos;
      const transform = getElementTransform(el);
      r.initialTransform = transform;
      setDragOffset({ x: transform.x, y: transform.y });
      r.isSwiping = true;
      r.isRealSwipe = false;
      r.lockedDirection = null;
      r.isFirstPointerMove = true;
      store.setHovering(true);
      r.dragAbortController?.abort();
      const dragAbortController = new AbortController();
      r.dragAbortController = dragAbortController;
      document.addEventListener("pointerup", handleSwipeEnd, { signal: dragAbortController.signal });
      document.addEventListener("pointercancel", handleSwipeEnd, { signal: dragAbortController.signal });
      el.setPointerCapture?.(event.pointerId);
    });

    el.addEventListener("pointermove", (event) => {
      if (event.pointerId !== r.activePointerId) return;
      // Prevent text selection on Safari
      event.preventDefault();
      if (r.isFirstPointerMove) {
        r.dragStartPos = { x: event.clientX, y: event.clientY };
        r.isFirstPointerMove = false;
      }
      const { clientY, clientX, movementX, movementY } = event;
      if ((movementY < 0 && clientY > r.swipeCancelBaseline.y) || (movementY > 0 && clientY < r.swipeCancelBaseline.y)) {
        r.swipeCancelBaseline = { x: r.swipeCancelBaseline.x, y: clientY };
      }
      if ((movementX < 0 && clientX > r.swipeCancelBaseline.x) || (movementX > 0 && clientX < r.swipeCancelBaseline.x)) {
        r.swipeCancelBaseline = { x: clientX, y: r.swipeCancelBaseline.y };
      }
      const deltaX = clientX - r.dragStartPos.x;
      const deltaY = clientY - r.dragStartPos.y;
      const cancelDeltaY = clientY - r.swipeCancelBaseline.y;
      const cancelDeltaX = clientX - r.swipeCancelBaseline.x;
      // React state set in this handler is read on the next event.
      const lockedDirection = r.lockedDirection;
      if (!r.isRealSwipe) {
        const movementDistance = Math.sqrt(deltaX * deltaX + deltaY * deltaY);
        if (movementDistance >= MIN_DRAG_THRESHOLD) {
          r.isRealSwipe = true;
          if (lockedDirection === null) {
            const hasHorizontal = swipeDirections.includes("left") || swipeDirections.includes("right");
            const hasVertical = swipeDirections.includes("up") || swipeDirections.includes("down");
            if (hasHorizontal && hasVertical) {
              r.lockedDirection = Math.abs(deltaX) > Math.abs(deltaY) ? "horizontal" : "vertical";
            }
          }
        }
      }
      let candidate;
      if (!r.intendedSwipeDirection) {
        if (lockedDirection === "vertical") {
          if (deltaY > 0) candidate = "down";
          else if (deltaY < 0) candidate = "up";
        } else if (lockedDirection === "horizontal") {
          if (deltaX > 0) candidate = "right";
          else if (deltaX < 0) candidate = "left";
        } else if (Math.abs(deltaX) >= Math.abs(deltaY)) {
          candidate = deltaX > 0 ? "right" : "left";
        } else {
          candidate = deltaY > 0 ? "down" : "up";
        }
        if (candidate && swipeDirections.includes(candidate)) {
          r.intendedSwipeDirection = candidate;
          r.maxSwipeDisplacement = getDisplacement(candidate, deltaX, deltaY);
          r.currentSwipeDirection = candidate;
        }
      } else {
        const direction = r.intendedSwipeDirection;
        const currentDisplacement = getDisplacement(direction, cancelDeltaX, cancelDeltaY);
        if (currentDisplacement > SWIPE_THRESHOLD) {
          r.cancelledSwipe = false;
          r.currentSwipeDirection = direction;
        } else if (
          !(swipeDirections.includes("left") && swipeDirections.includes("right")) &&
          !(swipeDirections.includes("up") && swipeDirections.includes("down")) &&
          r.maxSwipeDisplacement - currentDisplacement >= REVERSE_CANCEL_THRESHOLD
        ) {
          // Mark that a change-of-mind has occurred
          r.cancelledSwipe = true;
        }
      }
      const dampedDelta = applyDirectionalDamping(deltaX, deltaY);
      let newOffsetX = r.initialTransform.x;
      let newOffsetY = r.initialTransform.y;
      const horizontal = swipeDirections.includes("left") || swipeDirections.includes("right");
      const vertical = swipeDirections.includes("up") || swipeDirections.includes("down");
      if (lockedDirection === "horizontal") {
        if (horizontal) newOffsetX += dampedDelta.x;
      } else if (lockedDirection === "vertical") {
        if (vertical) newOffsetY += dampedDelta.y;
      } else {
        if (horizontal) newOffsetX += dampedDelta.x;
        if (vertical) newOffsetY += dampedDelta.y;
      }
      setDragOffset({ x: newOffsetX, y: newOffsetY });
      provider.render();
    });
    el.addEventListener("pointerup", handleSwipeEnd);
    el.addEventListener("pointercancel", handleSwipeEnd);

    el.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        if (!contains(el, document.activeElement)) return;
        store.closeToast(r.id);
      }
    });

    // React's pointermove preventDefault is not enough on iOS.
    el.addEventListener(
      "touchmove",
      (event) => {
        if (r.activePointerId === null || !contains(el, event.target)) return;
        event.preventDefault();
      },
      { passive: false },
    );

    // ToastAction: the toast's actionProps.
    action.addEventListener("click", (event) => toastOf()?.actionProps?.onClick?.(event));

    // ToastClose
    close.addEventListener("click", () => store.closeToast(r.id));
    close.addEventListener("focus", () => {
      r.closeHasFocus = true;
      provider.render();
    });
    close.addEventListener("blur", () => {
      r.closeHasFocus = false;
      provider.render();
    });

    // ToastContent: recalculate the height when the content changes.
    r.observers = [
      new ResizeObserver(() => r.recalculateHeight()),
      new MutationObserver(() => r.recalculateHeight()),
    ];
    return r;
  }

  function getDragStyles(r) {
    if (
      !r.isSwiping &&
      r.dragOffset.x === r.initialTransform.x &&
      r.dragOffset.y === r.initialTransform.y &&
      !r.dragDismissed
    ) {
      return { transition: "", transform: "", x: "0px", y: "0px" };
    }
    const deltaX = r.dragOffset.x - r.initialTransform.x;
    const deltaY = r.dragOffset.y - r.initialTransform.y;
    return {
      transition: r.isSwiping ? "none" : "",
      // While swiping, freeze the element at its current visual transform.
      transform: r.isSwiping
        ? `translateX(${r.dragOffset.x}px) translateY(${r.dragOffset.y}px) scale(${r.initialTransform.scale})`
        : "",
      x: `${deltaX}px`,
      y: `${deltaY}px`,
    };
  }

  function setAttr(el, name, value) {
    if (value === undefined || value === null || value === false) el.removeAttribute(name);
    else el.setAttribute(name, value === true ? "" : String(value));
  }

  // Renders one toast like ToastRoot and its parts render from the state.
  function renderToast(provider, r, toast) {
    const { store, portal } = provider;
    const state = store.state;
    const el = r.el;
    const expanded = selectors.expanded(state);
    const domIndex = selectors.toastIndex(state, toast.id);
    const visibleIndex = selectors.toastVisibleIndex(state, toast.id);
    const offsetY = selectors.toastOffsetY(state, toast.id);
    const isHighPriority = toast.priority === "high";

    // ToastIcon
    const iconTemplate = toast.type ? portal.querySelector(`template[data-templ-toast-icon="${toast.type}"]`) : null;
    if (r.iconType !== toast.type || (!r.icon && iconTemplate)) {
      r.icon?.remove();
      r.icon = iconTemplate ? iconTemplate.content.firstElementChild.cloneNode(true) : null;
      if (r.icon) r.content.prepend(r.icon);
      r.iconType = toast.type;
    }

    // ToastTitle and ToastDescription render when they have children.
    r.titleId = toast.title ? r.ownTitleId : undefined;
    if (toast.title) {
      if (!r.title.isConnected) r.titleText.prepend(r.title);
      r.title.id = r.ownTitleId;
      if (r.title.textContent !== String(toast.title)) r.title.textContent = toast.title;
      setAttr(r.title, "data-type", toast.type);
    } else {
      r.title.remove();
    }
    r.descriptionId = toast.description ? r.ownDescriptionId : undefined;
    if (toast.description) {
      if (!r.description.isConnected) r.titleText.append(r.description);
      r.description.id = r.ownDescriptionId;
      if (r.description.textContent !== String(toast.description)) r.description.textContent = toast.description;
      setAttr(r.description, "data-type", toast.type);
    } else {
      r.description.remove();
    }

    // ToastAction renders when the toast's actionProps have children.
    const actionChildren = toast.actionProps?.children;
    if (actionChildren) {
      if (!r.action.isConnected) r.content.insertBefore(r.action, r.close);
      if (r.action.textContent !== String(actionChildren)) r.action.textContent = actionChildren;
      setAttr(r.action, "data-type", toast.type);
    } else {
      r.action.remove();
    }

    // ToastClose
    r.close.setAttribute("aria-hidden", String(!expanded && !r.closeHasFocus));
    setAttr(r.close, "data-type", toast.type);

    // ToastContent
    setAttr(r.content, "data-expanded", expanded);
    setAttr(r.content, "data-behind", visibleIndex > 0);

    // ToastRoot
    el.setAttribute("role", isHighPriority ? "alertdialog" : "dialog");
    el.setAttribute("tabindex", "0");
    el.setAttribute("aria-modal", "false");
    setAttr(el, "aria-labelledby", r.titleId);
    setAttr(el, "aria-describedby", r.descriptionId);
    setAttr(el, "aria-hidden", isHighPriority && !state.focused ? "true" : undefined);
    setAttr(el, "inert", !!toast.limited);
    const drag = getDragStyles(r);
    el.style.transition = drag.transition;
    el.style.transform = drag.transform;
    el.style.setProperty("--toast-swipe-movement-x", drag.x);
    el.style.setProperty("--toast-swipe-movement-y", drag.y);
    el.style.setProperty("--toast-index", String(toast.transitionStatus === "ending" ? domIndex : visibleIndex));
    el.style.setProperty("--toast-offset-y", `${offsetY}px`);
    if (toast.height) el.style.setProperty("--toast-height", `${toast.height}px`);
    else el.style.removeProperty("--toast-height");
    setAttr(el, "data-starting-style", toast.transitionStatus === "starting");
    setAttr(el, "data-ending-style", toast.transitionStatus === "ending");
    setAttr(el, "data-expanded", expanded);
    setAttr(el, "data-limited", !!toast.limited);
    setAttr(el, "data-type", toast.type);
    setAttr(el, "data-swiping", r.isSwiping);
    setAttr(el, "data-swipe-direction", r.currentSwipeDirection);
  }

  // ----- Provider and Viewport ----------------------------------------------

  function createProvider(viewport) {
    const portal = viewport.parentElement;
    const provider = { portal, viewport, toasts: new Map() };
    const store = new ToastStore(
      {
        timeout: parseInt(viewport.getAttribute("data-templ-timeout"), 10) || 5000,
        limit: parseInt(viewport.getAttribute("data-templ-limit"), 10) || 3,
        viewport,
        toasts: [],
        hovering: false,
        focused: false,
        isWindowFocused: true,
        prevFocusElement: null,
      },
      () => provider.render(),
    );
    provider.store = store;

    const windowFocusTimeout = createTimeout();
    let handlingFocusGuard = false;
    let markedReadyForMouseLeave = false;
    let touchActive = false;
    let hadTransitioningToasts = false;
    let listening = false;
    const guards = { outside: null, before: null, after: null };
    let alerts = null;

    function handleFocusGuard(event) {
      handlingFocusGuard = true;
      // Coming off the container, move to the first toast that can hold focus.
      if (event.relatedTarget === viewport) {
        const firstFocusableToast = store.state.toasts.find((t) => t.transitionStatus !== "ending" && !t.limited);
        if (firstFocusableToast) firstFocusableToast.ref?.current?.focus();
        else store.restoreFocusToPrevElement();
      } else {
        store.restoreFocusToPrevElement();
      }
    }

    function flushMouseLeave() {
      const hasEndingToasts = store.state.toasts.some((t) => t.transitionStatus === "ending");
      if (hasEndingToasts || touchActive || !markedReadyForMouseLeave) return;
      if (store.state.isWindowFocused) store.resumeTimers();
      markedReadyForMouseLeave = false;
      store.setHovering(false);
    }

    function handleMouseEnter() {
      store.pauseTimers();
      markedReadyForMouseLeave = false;
      if (!store.state.hovering) store.setHovering(true);
    }

    function resumeTimersIfWindowFocused() {
      if (store.state.isWindowFocused) store.resumeTimers();
    }

    function handleFocus() {
      if (handlingFocusGuard) {
        handlingFocusGuard = false;
        return;
      }
      if (store.state.focused) return;
      // Only set focused when the active element is focus-visible.
      if (isFocusVisible(document.activeElement)) {
        store.setFocused(true);
        store.pauseTimers();
      }
    }

    viewport.addEventListener("mouseenter", handleMouseEnter);
    viewport.addEventListener("mousemove", handleMouseEnter);
    viewport.addEventListener("mouseleave", () => {
      const hasEndingToasts = store.state.toasts.some((t) => t.transitionStatus === "ending");
      if (hasEndingToasts || touchActive) {
        // Wait until the transitions have settled or the touch ends.
        markedReadyForMouseLeave = true;
      } else {
        resumeTimersIfWindowFocused();
        store.setHovering(false);
      }
    });
    viewport.addEventListener("focusin", handleFocus);
    viewport.addEventListener("click", handleFocus);
    viewport.addEventListener("focusout", (event) => {
      if (!store.state.focused || contains(viewport, event.relatedTarget)) return;
      store.setFocused(false);
      resumeTimersIfWindowFocused();
    });
    viewport.addEventListener("keydown", (event) => {
      if (event.key === "Tab" && event.shiftKey && event.target === viewport) {
        event.preventDefault();
        store.restoreFocusToPrevElement();
        store.resumeTimers();
      }
    });
    viewport.addEventListener("pointerdown", (event) => {
      if (event.pointerType === "touch") touchActive = true;
    });
    const handlePointerEnd = (event) => {
      if (event.pointerType !== "touch") return;
      touchActive = false;
      flushMouseLeave();
    };
    viewport.addEventListener("pointerup", handlePointerEnd);
    viewport.addEventListener("pointercancel", handlePointerEnd);

    // Listen globally for F6 so we can force-focus the viewport.
    window.addEventListener("keydown", (event) => {
      if (selectors.isEmpty(store.state)) return;
      if (event.key === "F6" && event.target !== viewport) {
        event.preventDefault();
        store.setPrevFocusElement(document.activeElement);
        viewport.focus({ preventScroll: true });
        store.pauseTimers();
        store.setFocused(true);
      }
    });

    // The window and document listeners live while there are toasts.
    function handleWindowBlur(event) {
      if (event.target !== window) return;
      store.setIsWindowFocused(false);
      store.pauseTimers();
    }
    function handleWindowFocus(event) {
      if (event.relatedTarget) return;
      const target = event.target;
      const activeEl = document.activeElement;
      if (target === window || !contains(viewport, target) || !isFocusVisible(activeEl)) store.resumeTimers();
      // Wait for the handleFocus event to fire.
      windowFocusTimeout.start(0, () => store.setIsWindowFocused(true));
    }
    function setListening(next) {
      if (next === listening) return;
      listening = next;
      const method = next ? "addEventListener" : "removeEventListener";
      window[method]("blur", handleWindowBlur, true);
      window[method]("focus", handleWindowFocus, true);
      document[method]("pointerdown", store.handleDocumentPointerDown, true);
    }

    function renderViewport() {
      const state = store.state;
      const isEmpty = selectors.isEmpty(state);
      setListening(!isEmpty);
      setAttr(viewport, "data-expanded", selectors.expanded(state));
      const frontmostHeight = state.toasts[0]?.height ?? 0;
      if (frontmostHeight) viewport.style.setProperty("--toast-frontmost-height", `${frontmostHeight}px`);
      else viewport.style.removeProperty("--toast-frontmost-height");

      // The focus guards around and inside the viewport.
      const showGuards = !isEmpty && !!state.prevFocusElement;
      const { createFocusGuard } = window.templ.focusManager;
      if (showGuards && !guards.outside) {
        guards.outside = createFocusGuard(null, handleFocusGuard);
        guards.before = createFocusGuard(null, handleFocusGuard);
        guards.after = createFocusGuard(null, handleFocusGuard);
        portal.insertBefore(guards.outside, viewport);
      } else if (!showGuards && guards.outside) {
        Object.keys(guards).forEach((k) => {
          guards[k].remove();
          guards[k] = null;
        });
      }

      // High priority toasts announce through a visually hidden alert.
      const highPriorityToasts = state.toasts.filter((t) => t.priority === "high");
      if (!state.focused && highPriorityToasts.length > 0) {
        if (!alerts) {
          alerts = document.createElement("div");
          alerts.style.cssText =
            "clip-path:inset(50%);overflow:hidden;white-space:nowrap;border:0;padding:0;width:1px;height:1px;margin:-1px;position:fixed;top:0;left:0";
        }
        if (alerts.previousElementSibling !== viewport) viewport.after(alerts);
        alerts.replaceChildren(
          ...highPriorityToasts.map((t) => {
            const alert = document.createElement("div");
            alert.setAttribute("role", "alert");
            alert.setAttribute("aria-atomic", "true");
            const titleEl = document.createElement("div");
            titleEl.textContent = t.title ?? "";
            const descriptionEl = document.createElement("div");
            descriptionEl.textContent = t.description ?? "";
            alert.append(titleEl, descriptionEl);
            return alert;
          }),
        );
      } else if (alerts) {
        alerts.remove();
      }
    }

    // ToastList: keyed by toast id, in the store's order.
    function renderList() {
      const state = store.state;
      const ids = new Set(state.toasts.map((t) => t.id));
      provider.toasts.forEach((r, id) => {
        if (ids.has(id)) return;
        r.observers.forEach((o) => o.disconnect());
        r.dragAbortController?.abort();
        r.el.remove();
        provider.toasts.delete(id);
      });
      const mounted = [];
      const desired = state.toasts.map((toast) => {
        let r = provider.toasts.get(toast.id);
        if (!r) {
          r = mountToast(provider, toast);
          provider.toasts.set(toast.id, r);
          mounted.push(r);
        }
        renderToast(provider, r, toast);
        return r.el;
      });
      if (guards.before) desired.unshift(guards.before);
      if (guards.after) desired.push(guards.after);
      let next = viewport.firstChild;
      desired.forEach((node) => {
        if (node === next) next = next.nextSibling;
        else viewport.insertBefore(node, next);
      });
      return mounted;
    }

    function renderOnce() {
      renderViewport();
      const mounted = renderList();
      // useOpenChangeComplete: an ending toast is removed once its exit
      // animations finished.
      provider.toasts.forEach((r, id) => {
        const toast = selectors.toast(store.state, id);
        if (toast?.transitionStatus === "ending" && !r.ending) {
          r.ending = true;
          window.templ.transition.animationsFinished(r.el, () => {
            if (selectors.toast(store.state, id)?.transitionStatus === "ending") store.removeToast(id);
          });
        }
      });
      // ToastContent's layout effect and observers.
      mounted.forEach((r) => {
        r.recalculateHeight();
        r.observers[0].observe(r.content);
        r.observers[1].observe(r.content, { childList: true, subtree: true, characterData: true });
      });
      const hasTransitioningToasts = store.state.toasts.some((t) => t.transitionStatus === "ending");
      if (hasTransitioningToasts !== hadTransitioningToasts) {
        hadTransitioningToasts = hasTransitioningToasts;
        flushMouseLeave();
      }
    }

    // A store update renders after the code that updated it, like React
    // renders after an event handler and runs the layout effects then.
    let scheduled = false;
    provider.render = () => {
      if (scheduled) return;
      scheduled = true;
      queueMicrotask(() => {
        scheduled = false;
        renderOnce();
      });
    };

    // subscribeToToastManager
    provider.unsubscribe = toastManager[" subscribe"](({ action, options }) => {
      const id = options.id;
      if (action === "promise" && options.promise) store.promiseToast(options.promise, options);
      else if (action === "update" && id) store.updateToast(id, options);
      else if (action === "close") store.closeToast(id);
      else store.addToast(options);
    });
    return provider;
  }

  const toastManager = createToastManager();

  window.templ.lifecycle.register(VIEWPORT, {
    init(viewport) {
      viewport._templToastProvider = createProvider(viewport);
    },
    destroy(viewport) {
      const provider = viewport._templToastProvider;
      if (!provider) return;
      provider.unsubscribe();
      provider.store.clearTimers();
    },
  });

  // The server side toast request: toastManager.add with its options.
  window.templ.lifecycle.register("[data-templ-toast]", {
    init(stub) {
      const options = {};
      const title = stub.getAttribute("data-templ-title");
      const description = stub.getAttribute("data-templ-description");
      const type = stub.getAttribute("data-templ-type");
      const timeout = parseInt(stub.getAttribute("data-templ-timeout"), 10);
      if (title) options.title = title;
      if (description) options.description = description;
      if (type) options.type = type;
      if (timeout) options.timeout = timeout;
      stub.remove();
      toastManager.add(options);
    },
  });

  // shadcn's toast export: the toast manager.
  window.templ = window.templ || {};
  window.templ.toast = toastManager;
})();

// components/tooltip/tooltip.js
(function () {
  const CONTENT = '[data-slot="tooltip-content"]';
  // Base UI's TooltipTrigger identifier; a disabled trigger renders
  // data-trigger-disabled instead, so it never opens.
  const TRIGGER = "[data-base-ui-tooltip-trigger]";

  // shadcn's TooltipPrimitive.Arrow has no slot; Base UI renders it
  // aria-hidden as the popup's last child.
  function arrowOf(content) {
    return content.querySelector(':scope > [aria-hidden="true"]:last-child');
  }

  // Base UI's TooltipPositioner, the popup's parent: it is portaled and
  // positioned and renders the open state.
  function positionerOf(content) {
    return content.parentElement;
  }

  function statusOf(content) {
    return { positioner: positionerOf(content), parts: [content], stateParts: [arrowOf(content)] };
  }

  function contentFor(trigger) {
    return document.getElementById(trigger.getAttribute("data-templ-tooltip-trigger"));
  }

  function triggerFor(content) {
    return document.querySelector(
      '[data-templ-tooltip-trigger="' + content.id + '"]',
    );
  }

  // The positioner's parent is the portal node, which moves to <body>
  // (shadcn portals it the same way).
  function portalNodeOf(content) {
    return positionerOf(content).parentElement;
  }

  // TooltipPortal mounts with the popup.
  function portal(content) {
    const node = portalNodeOf(content);
    window.templ.portal.render(node);
    node.hidden = false;
  }

  // TooltipPositioner: useAnchorPositioning with the popup collision
  // avoidance, while the tooltip is mounted.
  function startAutoPositioning(content, trigger) {
    stopAutoPositioning(content);
    const positioner = positionerOf(content);
    const arrow = arrowOf(content);
    const positioning = window.templ.anchorPositioning.useAnchorPositioning({
      anchor: trigger,
      positioner,
      parts: [positioner, content, arrow],
      arrow,
      side: positioner.getAttribute("data-templ-side") || "top",
      align: positioner.getAttribute("data-templ-align") || "center",
      sideOffset: parseFloat(positioner.getAttribute("data-templ-side-offset")) || 0,
      alignOffset: parseFloat(positioner.getAttribute("data-templ-align-offset")) || 0,
    });
    content._templPositionCleanup = positioning.cleanup;
    return positioning.positioned;
  }

  function stopAutoPositioning(content) {
    if (!content._templPositionCleanup) return;
    content._templPositionCleanup();
    content._templPositionCleanup = null;
  }

  // ----- TooltipProvider -----------------------------------------------------

  // shadcn's layout wraps the page in TooltipProvider (delay 0), Base UI's
  // FloatingDelayGroup with its 400 ms timeout: a tooltip that opens while
  // another one is open, or within the timeout after it closed, opens in the
  // instant phase, and the other closes at once.
  const GROUP_TIMEOUT = 400;
  const group = { current: null, timer: null };

  // TooltipRoot's instantType: "delay" in the instant phase, or while closing
  // because another tooltip opened; else the open change's, "focus" for a
  // focus open, "dismiss" for a press or Escape, none for hover. Positioner,
  // popup and arrow render it as data-instant.
  function setInstantType(content, open, reason) {
    if (open && reason === "trigger-focus") content._templInstantType = "focus";
    else if (!open && (reason === "trigger-press" || reason === "escape-key")) content._templInstantType = "dismiss";
    else if (reason === "trigger-hover") content._templInstantType = undefined;
    content._templCloseReason = open ? null : reason ?? null;
    renderInstant(content);
  }

  function renderInstant(content) {
    const delay = content._templEnding ? content._templCloseReason === "none" : !!content._templInstantPhase;
    const type = delay ? "delay" : content._templInstantType;
    [positionerOf(content), content, arrowOf(content)].forEach((el) => {
      if (!el) return;
      if (type) el.setAttribute("data-instant", type);
      else el.removeAttribute("data-instant");
    });
  }

  function setInstantPhase(content, on) {
    content._templInstantPhase = on;
    renderInstant(content);
  }

  // The group's open side: this tooltip becomes the current one.
  function joinGroup(content) {
    clearTimeout(group.timer);
    const previous = group.current;
    group.current = content;
    if (previous && previous !== content) {
      setInstantPhase(content, true);
      setInstantPhase(previous, true);
      requestOpenChange(triggerFor(previous), false, { reason: "none" });
    } else {
      setInstantPhase(content, false);
    }
  }

  // The group's close side: the current tooltip ends the instant phase after
  // the timeout, unless another one opened meanwhile.
  function leaveGroup(content) {
    if (group.current !== content) return;
    setInstantPhase(content, false);
    clearTimeout(group.timer);
    group.timer = setTimeout(() => {
      if (group.current !== content || content._templOpen) return;
      group.current = null;
    }, GROUP_TIMEOUT);
  }

  // ----- open / close ----------------------------------------------------------

  // details { reason, event } of the change, for the hover interaction.
  function open(trigger, details = {}) {
    // A disabled trigger (Base UI data-trigger-disabled) never opens, e.g.
    // the sidebar's menu tooltips while it is expanded.
    if (trigger.hasAttribute("data-trigger-disabled")) return;
    const content = contentFor(trigger);
    if (!content) return;
    content._templOpen = true;
    content._templOpenEventType = details.event?.type ?? null;
    content._templEnding = false;
    setInstantType(content, true, details.reason);
    joinGroup(content);
    portal(content);
    positionerOf(content).hidden = false;
    // TooltipRoot's useDismiss: a press on the trigger closes (closeOnClick).
    content._templDismiss ??= window.templ.dismiss.useDismiss({
      floating: positionerOf(content),
      reference: trigger,
      referencePress: true,
      onOpenChange: (open, reason, event) => requestOpenChange(trigger, open, { reason, event }),
    });
    emitOpenChange(content, true, details.reason);

    // Positioned first, then the enter animation plays in place.
    startAutoPositioning(content, trigger).then(() => {
      if (positionerOf(content).hidden) return; // closed meanwhile
      window.templ.transition.open(statusOf(content));
      trigger.setAttribute("data-popup-open", "");
    });
  }

  function close(content, details = {}) {
    if (positionerOf(content).hidden) return;
    content._templOpen = false;
    content._templOpenEventType = null;
    content._templEnding = true;
    setInstantType(content, false, details.reason);
    leaveGroup(content);
    emitOpenChange(content, false, details.reason);
    content._templDismiss?.();
    content._templDismiss = null;
    // Positioned until it unmounts, like Base UI.
    window.templ.transition.close(statusOf(content), content, () => {
      stopAutoPositioning(content);
      positionerOf(content).hidden = true;
      portalNodeOf(content).hidden = true;
      content._templEnding = false;
      content._templInstantPhase = false;
      renderInstant(content);
    });
    const trigger = triggerFor(content);
    if (trigger) trigger.removeAttribute("data-popup-open");
  }

  function requestOpenChange(trigger, nextOpen, details) {
    if (!trigger) return false;
    const content = contentFor(trigger);
    if (!content || !!content._templOpen === nextOpen) return false;
    const accepted = content.dispatchEvent(
      new CustomEvent("tooltip-open-change", {
        bubbles: true,
        cancelable: true,
        detail: { open: nextOpen },
      }),
    );
    if (!accepted || content.hasAttribute("data-templ-open")) return false;
    if (nextOpen) open(trigger, details);
    else close(content, details);
    return true;
  }

  // TooltipTrigger's useHoverReferenceInteraction and TooltipPopup's
  // useHoverFloatingInteraction. The delay is shadcn's TooltipProvider
  // default of 0, the popup is hoverable through safePolygon.
  function startHover(content, trigger) {
    const hover = window.templ.hover;
    const positioner = positionerOf(content);
    content._templHover = hover.createHoverInteraction({
      isOpen: () => !!content._templOpen,
      onOpenChange: (open, reason, event) => requestOpenChange(trigger, open, { reason, event }),
      openEventType: () => content._templOpenEventType ?? null,
      domReference: () => trigger,
      floating: () => (positioner.hidden ? null : positioner),
      placement: () => positioner.getAttribute("data-side") || "top",
      triggers: () => [trigger],
      parentFloating: () => null,
    });
    const isClosing = () => window.templ.transition.isEnding(content);
    content._templHoverCleanups = [
      hover.useHoverReferenceInteraction(trigger, content._templHover, {
        mouseOnly: true,
        move: false,
        handleClose: hover.safePolygon(),
        restMs: 0,
        delay: { close: 0 },
        isClosing,
      }),
      hover.useHoverFloatingInteraction(content._templHover, { closeDelay: 0 }),
    ];
    // TooltipTrigger's useFocus.
    content._templFocusOpen = window.templ.focus.useFocus(trigger, content._templHover.context);
    // TooltipTrigger's onPointerDown and onClick: with closeOnClick a press
    // cancels a pending open (cancelPendingOpen).
    const cancelPendingOpen = () => {
      if (!content._templOpen) emitOpenChange(content, false, "trigger-press");
    };
    trigger.addEventListener("pointerdown", cancelPendingOpen);
    trigger.addEventListener("click", cancelPendingOpen);
    content._templHoverCleanups.push(() => {
      trigger.removeEventListener("pointerdown", cancelPendingOpen);
      trigger.removeEventListener("click", cancelPendingOpen);
    });
  }

  // The store's openchange event, for the trigger's interactions.
  function emitOpenChange(content, open, reason) {
    content._templHover?.openChange(open, reason);
    content._templFocusOpen?.openChange(open, reason);
  }

  function stopHover(content) {
    content._templFocusOpen?.cleanup();
    content._templFocusOpen = null;
    content._templHoverCleanups?.forEach((cleanup) => cleanup());
    content._templHoverCleanups = null;
    content._templHover?.dispose();
    content._templHover = null;
  }

  // ----- events -------------------------------------------------------------

  // Content stays in its hidden portal node until it opens. It unmounts with
  // its portal owner: a portaled one is removed from <body> then.
  window.templ.lifecycle.register(CONTENT, {
    init(content) {
      const trigger = triggerFor(content);
      if (!trigger) return;
      startHover(content, trigger);
      // Server-side open state (Base UI open or defaultOpen).
      if (content.getAttribute("data-templ-open") === "true" || content.hasAttribute("data-templ-default-open")) {
        open(trigger);
      }
    },
    destroy(content) {
      stopHover(content);
      stopAutoPositioning(content);
      content._templDismiss?.();
      window.templ.portal.remove(portalNodeOf(content));
    },
  });
})();

