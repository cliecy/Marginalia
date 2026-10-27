/**
 * Page scaffolding for the views beside the sidebar.
 *
 * <Page>             scrolls, centres its content at a readable width
 * <PageIntro>        the short muted line under the top bar that says what a page is for
 * <Card>             a white card with a hairline (padding 14/16)
 * <Skeleton>         a shimmering placeholder block
 */

/**
 * @param {object} props
 * @param {"narrow"|"default"|"wide"|"full"} [props.width] 720 / 980 / 1200 px / no limit
 * @param {boolean} [props.flush] no inner padding (full-bleed canvases)
 */
export function Page({ width = "default", flush = false, className = "", children, ...rest }) {
  return (
    <div className={`page page-${width} ${flush ? "page-flush" : ""} ${className}`.replace(/\s+/g, " ").trim()} {...rest}>
      <div className="page-inner">{children}</div>
    </div>
  );
}

/** The page's one-line introduction, 12.5px muted (Magpie's intro lines). */
export function PageIntro({ children, className = "" }) {
  return <p className={`page-intro ${className}`.trim()}>{children}</p>;
}

/** A white card with a hairline border and radius 12. */
export function Card({ children, className = "", padded = true, ...rest }) {
  return (
    <div className={`card ${padded ? "card-pad" : ""} ${className}`.replace(/\s+/g, " ").trim()} {...rest}>
      {children}
    </div>
  );
}

/** A loading placeholder; give it a width and height. */
export function Skeleton({ width = "100%", height = 10, radius, className = "", style }) {
  return <i className={`skeleton ${className}`.trim()} style={{ width, height, borderRadius: radius, ...style }} aria-hidden="true" />;
}

export default Page;
