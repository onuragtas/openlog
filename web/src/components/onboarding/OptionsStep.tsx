import { useId } from "react";
import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import type { InstallOptions, InstallTarget, OptionKey } from "@/lib/install-commands";
import { tDynamic } from "@/lib/onboarding-key";

/** Select options: translation group under addData.options and the values in order. */
const SELECTS: Partial<Record<OptionKey, { labels: string; values: readonly string[] }>> = {
  distro: { labels: "distros", values: ["auto", "deb", "rpm", "tarball"] },
  channel: { labels: "channels", values: ["stable", "beta"] },
  nodeModules: { labels: "nodeModulesValues", values: ["commonjs", "esm"] },
  pythonLauncher: { labels: "pythonLaunchers", values: ["python", "gunicorn", "uvicorn", "django", "celery"] },
  javaMode: { labels: "javaModes", values: ["jvm", "docker"] },
  dotnetApp: { labels: "dotnetApps", values: ["aspnet", "console"] },
  phpMode: { labels: "phpModes", values: ["package", "fleet"] },
  phpPackage: { labels: "phpPackages", values: ["deb", "rpm", "apk"] },
  arch: { labels: "archs", values: ["amd64", "arm64"] },
  otelLanguage: { labels: "otelLanguages", values: ["node", "python", "java", "dotnet", "go", "other"] },
  protocol: { labels: "protocols", values: ["http", "grpc"] },
};

const CHECKBOXES: readonly OptionKey[] = ["dockerAccess", "journald"];

const HELP: Partial<Record<OptionKey, string>> = {
  hostName: "hostNameHelp",
  clusterName: "clusterNameHelp",
  serviceName: "serviceNameHelp",
  environment: "environmentHelp",
  dockerAccess: "dockerAccessHelp",
  browserOrigin: "browserOriginHelp",
};

const PLACEHOLDERS: Partial<Record<OptionKey, string>> = {
  hostName: "web-1",
  clusterName: "prod-eu-1",
  serviceName: "checkout",
  environment: "production",
  browserOrigin: "https://app.example.com",
};

/** Step 2: the card's options (service name, environment, package method, …). */
export function OptionsStep({ target, value, onChange }: { target: InstallTarget; value: InstallOptions; onChange: (v: InstallOptions) => void }) {
  const { t } = useTranslation();
  const id = useId();
  const keys = target.options.filter((k) => !((k === "phpPackage" || k === "arch") && value.phpMode === "fleet"));
  if (keys.length === 0) return <p className="text-sm text-muted-foreground">{t("addData.options.none")}</p>;
  const set = (k: OptionKey, v: string | boolean) => onChange({ ...value, [k]: v } as InstallOptions);

  return (
    <div className="grid min-w-0 grid-cols-1 gap-4 sm:grid-cols-2">
      {keys.map((k) => {
        const fid = `${id}-${k}`;
        const label = tDynamic(t, `addData.options.${k}`);
        const help = HELP[k] ? tDynamic(t, `addData.options.${HELP[k]}`) : undefined;
        const helpId = help ? `${fid}-help` : undefined;
        if (CHECKBOXES.includes(k)) {
          return (
            <div key={k} className="flex min-w-0 flex-col gap-1 sm:col-span-2">
              <label htmlFor={fid} className="flex cursor-pointer items-start gap-2 text-sm font-medium">
                <input
                  id={fid}
                  type="checkbox"
                  checked={value[k] as boolean}
                  onChange={(e) => set(k, e.target.checked)}
                  className="mt-0.5 size-4 shrink-0 accent-primary pointer-coarse:size-5"
                  aria-describedby={helpId}
                />
                {label}
              </label>
              {help && (
                <p id={helpId} className="pl-6 text-xs text-muted-foreground">
                  {help}
                </p>
              )}
            </div>
          );
        }
        const select = SELECTS[k];
        if (select) {
          return (
            <div key={k} className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={fid}>{label}</Label>
              <NativeSelect id={fid} className="w-full" value={value[k] as string} onChange={(e) => set(k, e.target.value)}>
                {select.values.map((v) => (
                  <option key={v} value={v}>
                    {tDynamic(t, `addData.options.${select.labels}.${v}`)}
                  </option>
                ))}
              </NativeSelect>
            </div>
          );
        }
        return (
          <div key={k} className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={fid}>{label}</Label>
            <Input
              id={fid}
              value={value[k] as string}
              placeholder={PLACEHOLDERS[k]}
              maxLength={k === "logPath" || k === "browserOrigin" ? 512 : 128}
              spellCheck={false}
              autoCapitalize="off"
              autoCorrect="off"
              aria-describedby={helpId}
              onChange={(e) => set(k, e.target.value)}
            />
            {help && (
              <p id={helpId} className="text-xs text-muted-foreground">
                {help}
              </p>
            )}
          </div>
        );
      })}
    </div>
  );
}
