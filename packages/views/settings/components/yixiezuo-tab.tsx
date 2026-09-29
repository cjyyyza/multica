"use client";

import { useT } from "../../i18n";
import { SettingsTab } from "./settings-layout";
import { YixiezuoImportForm } from "../../issues/components/yixiezuo-import";

export function YixiezuoTab() {
  const { t } = useT("settings");
  return (
    <SettingsTab title={t(($) => $.yixiezuo.section_title)} scope="workspace">
      <YixiezuoImportForm />
    </SettingsTab>
  );
}
