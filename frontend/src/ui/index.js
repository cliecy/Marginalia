/**
 * The app's UI kit, in Magpie's design language. Import from here:
 *
 *   import { Button, List, Row, Segmented, toast, confirm } from "../ui/index.js";
 *
 * Styles live in src/styles/components.css (class names documented in
 * scratchpad/frontend-contract.md).
 */
export { default as Icon, Logo, ICON_NAMES } from "./Icon.jsx";
export { default as Button, IconButton } from "./Button.jsx";
export { Pill, Badge } from "./Pill.jsx";
export { default as Segmented } from "./Segmented.jsx";
export { default as Toggle } from "./Toggle.jsx";
export { List, Row, ListFoot, SectionHead } from "./List.jsx";
export { default as Stats } from "./Stats.jsx";
export { Field, Input, TextArea, SearchInput, Select } from "./Field.jsx";
export { Popover, Menu, MenuButton, MenuHost, openMenu, closeMenu, contextMenu } from "./Popover.jsx";
export { Dialog, DialogHost, prompt, confirm, alert } from "./Dialog.jsx";
export { toast, dismissToast, ToastHost } from "./Toast.jsx";
export { default as EmptyState } from "./EmptyState.jsx";
export { CopyField, CopyButton, copyText } from "./CopyField.jsx";
export { default as CodeTabs } from "./CodeTabs.jsx";
export { default as StatusDot } from "./StatusDot.jsx";
export { default as Kbd, formatKeys } from "./Kbd.jsx";
export { isComposingKey } from "./keys.js";
export { default as Spinner } from "./Spinner.jsx";
export { Page, PageIntro, Card, Skeleton } from "./Page.jsx";
