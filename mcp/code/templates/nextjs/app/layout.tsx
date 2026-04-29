import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: 'Apteva App',
  description: 'A new Next.js app on Apteva.',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
