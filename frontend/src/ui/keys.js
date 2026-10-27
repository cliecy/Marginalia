/**
 * True while a key press belongs to an input method (Japanese, Chinese…)
 * rather than to the field: Enter then confirms a conversion and must not
 * send, search or commit.
 *
 * WebKit (Safari and the desktop app's webview) fires the keydown of the Enter
 * that commits a composition after compositionend, with isComposing already
 * false — but with keyCode 229, which marks every key the IME handled.
 *
 * @param {KeyboardEvent | import("react").KeyboardEvent} event
 */
export const isComposingKey = (event) =>
  !!(event?.nativeEvent?.isComposing || event?.isComposing || event?.keyCode === 229 || event?.nativeEvent?.keyCode === 229);
