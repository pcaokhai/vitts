import type { Metadata } from "next";

import { KeyProvider } from "@/components/KeyProvider";
import { Shell } from "@/components/Shell";

import "./globals.css";
import "./console.css";

export const metadata: Metadata = {
  title: "ViTTS Console",
  description: "Usage and API keys for the ViTTS Vietnamese speech API.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <KeyProvider>
          <Shell>{children}</Shell>
        </KeyProvider>
      </body>
    </html>
  );
}
