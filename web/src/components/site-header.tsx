import type { ReactNode } from "react"

import { SidebarTrigger } from "@/components/ui/sidebar"

/**
 * The title of the screen and, when a screen has one, the range it is
 * showing. Nothing else lives up here.
 */
export function SiteHeader({ title, aside }: { title: string; aside?: ReactNode }) {
  return (
    <header className="sticky top-0 z-10 flex h-12 shrink-0 items-center gap-2 border-b border-border bg-background px-3 print:hidden sm:px-4">
      <SidebarTrigger className="-ml-1" />
      <h1 className="text-sm font-medium">{title}</h1>
      {aside && (
        <div className="ml-auto min-w-0 truncate text-xs text-muted-foreground">{aside}</div>
      )}
    </header>
  )
}
