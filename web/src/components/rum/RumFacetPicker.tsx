// Which kind of visitor the screen is about: a device, a browser, or every one of them.
//
// "Is the site fast" and "is it fast on phones" are different questions, and the second is the one asked
// right after the first number disappoints. The options are not fetched: the SDK derives both from the
// user agent and writes a small fixed set (rum.md §2), so a list endpoint would be a request to learn
// four words.
import { useTranslation } from "react-i18next";
import type { RumFacets } from "@/api/rum";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";

export const DEVICE_TYPES = ["desktop", "mobile", "tablet"] as const;
export const BROWSERS = ["Chrome", "Safari", "Firefox", "Edge", "Opera", "Samsung Internet"] as const;

export function RumFacetPicker({
  facets,
  onChange,
  browsers = true,
  id,
}: {
  facets: RumFacets;
  onChange: (facets: RumFacets) => void;
  /** False where the data cannot answer it: the vitals rollup keeps no browser (rum.md §7). */
  browsers?: boolean;
  id: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-end gap-2">
      <div className="flex min-w-0 flex-col gap-1.5">
        <Label htmlFor={`${id}-device`} className="text-xs text-muted-foreground">
          {t("rum.facets.device")}
        </Label>
        <NativeSelect
          id={`${id}-device`}
          className="w-40"
          value={facets.device_type ?? ""}
          onChange={(e) => onChange({ ...facets, device_type: e.target.value || undefined })}
        >
          <option value="">{t("rum.facets.allDevices")}</option>
          {DEVICE_TYPES.map((d) => (
            <option key={d} value={d}>
              {t(`rum.facets.devices.${d}`)}
            </option>
          ))}
        </NativeSelect>
      </div>
      {browsers && (
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-browser`} className="text-xs text-muted-foreground">
            {t("rum.facets.browser")}
          </Label>
          <NativeSelect
            id={`${id}-browser`}
            className="w-40"
            value={facets.browser ?? ""}
            onChange={(e) => onChange({ ...facets, browser: e.target.value || undefined })}
          >
            <option value="">{t("rum.facets.allBrowsers")}</option>
            {BROWSERS.map((b) => (
              <option key={b} value={b}>
                {b}
              </option>
            ))}
          </NativeSelect>
        </div>
      )}
    </div>
  );
}
