"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { PopoBindPage } from "@multica/views/popo";

function PopoBindPageContent() {
  const searchParams = useSearchParams();
  const token = searchParams.get("token");
  return <PopoBindPage token={token} />;
}

export default function Page() {
  return (
    <Suspense fallback={null}>
      <PopoBindPageContent />
    </Suspense>
  );
}
