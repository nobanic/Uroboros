// Camera changes affect presentation only. Fixed card geometry, recorded data,
// layout, selection and saved-view documents stay with their existing owners.
export function graphPaint(zoom: number) {
  const scale = Number.isFinite(zoom) && zoom > 0 ? zoom : 1;
  const bounded = (minimum: number, pixels: number, maximum: number) =>
    Math.min(maximum, Math.max(minimum, pixels / scale));
  const title = bounded(14, 10, 40);
  return {
    tier: scale < 0.35 ? "far" : scale < 0.72 ? "medium" : "near",
    properties: {
      "--graph-title-font": `${title}px`,
      "--graph-title-leading": `${Math.ceil(title * 1.18)}px`,
      "--graph-kind-font": `${bounded(11, 6.6, 15)}px`,
      "--graph-meta-font": `${bounded(11, 7, 16)}px`,
      "--graph-relation-font": `${bounded(10, 8, 18)}px`,
      "--graph-stroke": `${bounded(1.35, 1.35, 3.4)}px`,
      "--graph-focus-stroke": `${bounded(2.4, 2.4, 5)}px`,
      "--graph-overview-kind-font": `${bounded(18, 7.4, 36)}px`,
      "--graph-overview-id-font": `${bounded(22, 8, 40)}px`,
    },
  };
}
