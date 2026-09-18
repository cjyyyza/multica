// Original integration mark for the POPO channel list. This is not the
// official POPO brand asset; lucide-react ships no brand icons, and every
// messaging channel needs its own mark (see WecomMark, #6585).
export function PopoMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      aria-hidden="true"
      className={className}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M7 5.5h10a3.5 3.5 0 0 1 3.5 3.5v5A3.5 3.5 0 0 1 17 17.5H11l-4 3v-3H7A3.5 3.5 0 0 1 3.5 14V9A3.5 3.5 0 0 1 7 5.5Z" />
      <circle cx="9" cy="11" r="1" fill="currentColor" stroke="none" />
      <circle cx="12" cy="11" r="1" fill="currentColor" stroke="none" />
      <circle cx="15" cy="11" r="1" fill="currentColor" stroke="none" />
    </svg>
  );
}
