import Link from "next/link";

import { Heart } from "lucide-react";

import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export function SidebarSupportCard() {
  return (
    <Card size="sm" className="uro-support-card group-data-[collapsible=icon]:hidden">
      <CardHeader className="min-w-0 px-4">
        <CardTitle className="text-sm">Keep Uroboros independent</CardTitle>
        <CardDescription>Your support helps keep Uroboros independent, free, and open source.</CardDescription>
        <Link
          href="https://ko-fi.com/C8S229C6V0"
          target="_blank"
          rel="noreferrer"
          aria-label="Support Uroboros on Ko-fi"
          className="uro-support-link"
        >
          <Heart aria-hidden className="size-3.5" />
          <span>Support me on Ko-fi</span>
        </Link>
      </CardHeader>
    </Card>
  );
}
