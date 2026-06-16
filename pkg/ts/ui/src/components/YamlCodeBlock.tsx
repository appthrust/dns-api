/** @jsxRuntime classic */
import hljs from 'highlight.js/lib/core';
import yamlLanguage from 'highlight.js/lib/languages/yaml';
import React from 'react';
import { Box } from './primitives';
import { token } from './style';

hljs.registerLanguage('yaml', yamlLanguage);

export function YamlCodeBlock({
  code,
  emptyText = 'No YAML',
  action,
  maxHeight = 420,
}: {
  code: string;
  emptyText?: string;
  action?: React.ReactNode;
  maxHeight?: number | string;
}) {
  const highlighted = React.useMemo(() => {
    const source = code.trim() ? code : emptyText;
    return hljs.highlight(source, { language: 'yaml' }).value;
  }, [code, emptyText]);

  return (
    <Box sx={{ position: 'relative' }}>
      <style>
        {`
          .dns-yaml-code .hljs-attr { color: var(--dns-ui-accent, #2563eb); }
          .dns-yaml-code .hljs-string { color: var(--dns-ui-success, #138a5b); }
          .dns-yaml-code .hljs-number,
          .dns-yaml-code .hljs-literal { color: var(--dns-ui-warning, #b7791f); }
          .dns-yaml-code .hljs-comment { color: var(--dns-ui-text-muted, #667085); }
        `}
      </style>
      {action ? <Box sx={{ position: 'absolute', right: 1, top: 1, zIndex: 1 }}>{action}</Box> : null}
      <Box
        className="dns-yaml-code"
        component="pre"
        sx={{
          bgcolor: 'fieldBg',
          border: 1,
          borderColor: 'borderSoft',
          borderRadius: 1,
          color: code.trim() ? 'text' : 'textMuted',
          fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
          fontSize: 13,
          lineHeight: 1.65,
          m: 0,
          maxHeight,
          overflow: 'auto',
          p: 2,
          pr: action ? 5 : 2,
          whiteSpace: 'pre-wrap',
        }}
      >
        <code
          dangerouslySetInnerHTML={{
            __html: highlighted,
          }}
        />
      </Box>
    </Box>
  );
}

export function yamlCodeBlockColorToken(name: string) {
  return token(name);
}
