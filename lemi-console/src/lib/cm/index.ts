// CodeMirror 6 公共配置：跟随深浅主题的编辑器外观与 SQL 高亮（颜色全部取 CSS 变量，切换主题不用重建编辑器）、
// MySQL / SQLite 方言、只读代码视图。控制台、DDL、设计器预览、筛选输入框共用。
import { EditorView, keymap, lineNumbers, highlightActiveLineGutter, highlightSpecialChars, drawSelection, dropCursor,
  rectangularSelection, crosshairCursor, highlightActiveLine, placeholder as cmPlaceholder } from '@codemirror/view'
import { EditorState, type Extension } from '@codemirror/state'
import { HighlightStyle, syntaxHighlighting, indentOnInput, bracketMatching, foldGutter, foldKeymap } from '@codemirror/language'
import { history, historyKeymap, defaultKeymap, indentWithTab } from '@codemirror/commands'
import { closeBrackets, closeBracketsKeymap, autocompletion, completionKeymap } from '@codemirror/autocomplete'
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search'
import { sql, MySQL, SQLite, type SQLNamespace } from '@codemirror/lang-sql'
import { tags as t } from '@lezer/highlight'
import type { Dialect } from '@/lib/sql'
import './cm.css'

export const lemiTheme = EditorView.theme({
  '&': { color: 'var(--text)', backgroundColor: 'var(--bg)', fontSize: '13px', height: '100%' },
  '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.6' },
  '.cm-content': { caretColor: 'var(--text)', padding: '6px 0' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--text)' },
  '&.cm-focused': { outline: 'none' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': { backgroundColor: 'var(--cm-selection) !important' },
  '.cm-gutters': { backgroundColor: 'var(--bg-soft)', color: 'var(--text-3)', border: 'none', borderRight: '1px solid var(--border)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--bg-hover)', color: 'var(--text-2)' },
  '.cm-activeLine': { backgroundColor: 'var(--cm-active-line)' },
  '.cm-selectionMatch': { backgroundColor: 'var(--cm-match)' },
  '&.cm-focused .cm-matchingBracket': { backgroundColor: 'var(--cm-match)', outline: '1px solid var(--border-strong)' },
  '.cm-foldPlaceholder': { backgroundColor: 'var(--bg-active)', border: 'none', color: 'var(--text-2)' },
  '.cm-tooltip': { backgroundColor: 'var(--bg-elev)', color: 'var(--text)', border: 'none', borderRadius: '6px', boxShadow: 'var(--shadow-pop)', overflow: 'hidden' },
  '.cm-tooltip-autocomplete > ul': { fontFamily: 'var(--font-mono)', fontSize: '12.5px', maxHeight: '18em' },
  '.cm-tooltip-autocomplete > ul > li': { padding: '2px 8px !important', lineHeight: '1.6' },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--accent-soft)', color: 'var(--text)' },
  '.cm-completionDetail': { color: 'var(--text-3)', fontStyle: 'normal', marginLeft: '8px' },
  '.cm-completionIcon': { opacity: '.55', width: '1.1em' },
  '.cm-panels': { backgroundColor: 'var(--bg-soft)', color: 'var(--text)' },
  '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--border)' },
  '.cm-searchMatch': { backgroundColor: 'var(--cm-match)', outline: '1px solid var(--border-strong)' },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'var(--accent-soft)' },
  '.cm-textfield': { backgroundColor: 'var(--bg)', border: '1px solid var(--border-strong)', borderRadius: '4px', color: 'var(--text)' },
  '.cm-button': { backgroundImage: 'none', backgroundColor: 'var(--bg)', border: '1px solid var(--border-strong)', borderRadius: '4px', color: 'var(--text)' },
  '.cm-placeholder': { color: 'var(--text-3)' },
})

export const lemiHighlight = HighlightStyle.define([
  { tag: [t.keyword, t.operatorKeyword, t.modifier], color: 'var(--cm-kw)', fontWeight: '500' },
  { tag: [t.string, t.special(t.string)], color: 'var(--cm-str)' },
  { tag: [t.number, t.bool, t.null], color: 'var(--cm-num)' },
  { tag: [t.lineComment, t.blockComment, t.comment], color: 'var(--cm-comment)', fontStyle: 'italic' },
  { tag: [t.function(t.variableName), t.standard(t.name), t.standard(t.variableName)], color: 'var(--cm-fn)' },
  { tag: [t.typeName, t.className], color: 'var(--cm-type)' },
  { tag: [t.special(t.name), t.special(t.variableName)], color: 'var(--cm-ident)' },
  { tag: [t.operator, t.compareOperator, t.arithmeticOperator, t.logicOperator], color: 'var(--cm-op)' },
  { tag: [t.punctuation, t.paren, t.bracket, t.separator], color: 'var(--text-2)' },
  { tag: [t.variableName, t.name, t.propertyName], color: 'var(--text)' },
  { tag: t.invalid, color: 'var(--danger)' },
])

/** SQL 语言扩展；schema 为 { 表: [列] }（来自结构快照），用于补全表、列与别名。 */
export function sqlLang(dialect: Dialect, schema?: SQLNamespace, defaultTable?: string): Extension {
  return sql({ dialect: dialect === 'sqlite' ? SQLite : MySQL, schema, defaultTable, upperCaseKeywords: true })
}

/** 完整编辑器的通用扩展（不含语言）。 */
export function editorBasics(opts: { lineNumbers?: boolean; placeholder?: string } = {}): Extension[] {
  return [
    opts.lineNumbers === false ? [] : [lineNumbers(), highlightActiveLineGutter(), foldGutter()],
    highlightSpecialChars(),
    history(),
    drawSelection(),
    dropCursor(),
    EditorState.allowMultipleSelections.of(true),
    indentOnInput(),
    syntaxHighlighting(lemiHighlight),
    bracketMatching(),
    closeBrackets(),
    autocompletion({ activateOnTyping: true, icons: false }),
    rectangularSelection(),
    crosshairCursor(),
    highlightActiveLine(),
    highlightSelectionMatches(),
    opts.placeholder ? cmPlaceholder(opts.placeholder) : [],
    keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...foldKeymap, ...completionKeymap, indentWithTab]),
    lemiTheme,
  ]
}

/** 只读代码视图（DDL、将执行的 SQL 预览）。 */
export function createReadonlyView(parent: HTMLElement, doc: string, dialect: Dialect, opts: { lineNumbers?: boolean; wrap?: boolean } = {}): EditorView {
  return new EditorView({
    parent,
    state: EditorState.create({
      doc,
      extensions: [
        opts.lineNumbers === false ? [] : [lineNumbers(), foldGutter()],
        highlightSpecialChars(),
        drawSelection(),
        syntaxHighlighting(lemiHighlight),
        highlightSelectionMatches(),
        keymap.of([...defaultKeymap, ...searchKeymap, ...foldKeymap]),
        EditorState.readOnly.of(true),
        EditorView.editable.of(false),
        EditorView.contentAttributes.of({ tabindex: '0' }),
        opts.wrap ? EditorView.lineWrapping : [],
        sqlLang(dialect),
        lemiTheme,
      ],
    }),
  })
}

export function setDoc(view: EditorView, doc: string) {
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: doc } })
}

export { copyText } from '@/lib/clipboard'
