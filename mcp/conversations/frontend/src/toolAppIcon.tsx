// AppIcon implementation from ui-kit/src/AppIdentity.tsx.
import {
  createContext,
  useContext,
  type CSSProperties,
  type ReactNode,
} from "react";

export type AppIconStyle = "image" | "monochrome";
export type AppIconSize = "xs" | "sm" | "md" | "lg" | "xl";

export interface AppIdentity {
  name: string;
  displayName: string;
  iconUrl?: string;
  iconStyle?: AppIconStyle | string;
}

export interface AppIdentityProviderProps {
  value: AppIdentity | null;
  children: ReactNode;
}

const AppIdentityContext = createContext<AppIdentity | null>(null);

export function AppIdentityProvider({ value, children }: AppIdentityProviderProps) {
  return (
    <AppIdentityContext.Provider value={value}>
      {children}
    </AppIdentityContext.Provider>
  );
}

export function useAppIdentity(): AppIdentity | null {
  return useContext(AppIdentityContext);
}

export interface AppIconProps {
  src?: string;
  iconStyle?: AppIconStyle | string;
  name: string;
  size?: AppIconSize;
  framed?: boolean;
  className?: string;
  decorative?: boolean;
}

const outerSize: Record<AppIconSize, string> = {
  xs: "h-4 w-4",
  sm: "h-6 w-6",
  md: "h-10 w-10",
  lg: "h-12 w-12",
  xl: "h-16 w-16",
};

// Unframed marks should use most of their allocation (sidebar/tool rows).
// Framed marks need the calmer gallery proportion: a 32px mark inside a
// 48px surface, with equivalent breathing room at every size.
const unframedInnerSize: Record<AppIconSize, string> = {
  xs: "h-3.5 w-3.5",
  sm: "h-5 w-5",
  md: "h-8 w-8",
  lg: "h-10 w-10",
  xl: "h-14 w-14",
};

const framedInnerSize: Record<AppIconSize, string> = {
  xs: "h-3 w-3",
  sm: "h-4 w-4",
  md: "h-7 w-7",
  lg: "h-8 w-8",
  xl: "h-11 w-11",
};

export function AppIcon({
  src,
  iconStyle,
  name,
  size = "sm",
  framed = true,
  className = "",
  decorative = true,
}: AppIconProps) {
  const monochrome = iconStyle === "monochrome";
  const hasSource = Boolean(src);
  const markSize = (framed ? framedInnerSize : unframedInnerSize)[size];
  const maskStyle: CSSProperties | undefined = src
    ? {
        WebkitMaskImage: `url(${JSON.stringify(src)})`,
        WebkitMaskPosition: "center",
        WebkitMaskRepeat: "no-repeat",
        WebkitMaskSize: "contain",
        maskImage: `url(${JSON.stringify(src)})`,
        maskPosition: "center",
        maskRepeat: "no-repeat",
        maskSize: "contain",
      }
    : undefined;

  return (
    <span
      key={src || name}
      className={`relative inline-flex shrink-0 items-center justify-center overflow-hidden ${
        outerSize[size]
      } ${framed ? "rounded-md bg-bg-input" : ""} ${className}`}
      aria-hidden={decorative || undefined}
      role={decorative ? undefined : "img"}
      aria-label={decorative ? undefined : name}
    >
      {hasSource && monochrome ? (
        <>
          <span
            className="text-[0.7em] font-medium uppercase text-current"
            style={{ visibility: "hidden" }}
          >
            {name.trim().charAt(0) || "A"}
          </span>
          <span
            className={`${markSize} absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 bg-current`}
            style={maskStyle}
          />
          <img
            src={src}
            alt=""
            className="hidden"
            onError={(event) => {
              const mask = event.currentTarget.previousElementSibling as HTMLElement | null;
              if (mask) mask.style.display = "none";
              const fallback = mask?.previousElementSibling as HTMLElement | null;
              if (fallback) fallback.style.visibility = "visible";
            }}
          />
        </>
      ) : hasSource ? (
        <>
          <span
            className="text-[0.7em] font-medium uppercase text-current"
            style={{ visibility: "hidden" }}
          >
            {name.trim().charAt(0) || "A"}
          </span>
          <img
            src={src}
            alt=""
            className={`${markSize} absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 object-contain`}
            onError={(event) => {
              event.currentTarget.style.display = "none";
              const fallback = event.currentTarget.previousElementSibling as HTMLElement | null;
              if (fallback) fallback.style.visibility = "visible";
            }}
          />
        </>
      ) : (
        <span className="text-[0.7em] font-medium uppercase text-current">
          {name.trim().charAt(0) || "A"}
        </span>
      )}
    </span>
  );
}
