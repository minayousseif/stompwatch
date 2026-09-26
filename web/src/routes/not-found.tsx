import { Link } from "react-router-dom"

import { Screen } from "@/components/app-shell"
import { SiteHeader } from "@/components/site-header"
import { Panel } from "@/components/state"
import { Button } from "@/components/ui/button"

export function NotFoundScreen() {
  return (
    <>
      <SiteHeader title="Not found" />
      <Screen>
        <Panel>
          <p className="text-sm text-foreground">There is no page at this address.</p>
          <Button asChild variant="outline" size="sm" className="mt-4">
            <Link to="/">Go to Live</Link>
          </Button>
        </Panel>
      </Screen>
    </>
  )
}
