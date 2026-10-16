#!/bin/bash
f=$1
stage() { r=$(env -u GOROOT go tool pprof -top -nodecount=100000 -nodefraction=0 -edgefraction=0 -focus="$2" ${3:+-ignore="$3"} "$f" 2>/dev/null | grep -m1 "Showing nodes accounting for" | sed -E 's/.*for ([^,]+), ([0-9.]+%).*/\1 \2/'); printf "%-50s %s\n" "$1" "${r:-0 0%}"; }
env -u GOROOT go tool pprof -top -nodecount=1 "$f" 2>/dev/null | grep -E "Duration"
stage "watch stream decode (StreamWatcher.receive)"  'StreamWatcher\)\.receive'
stage "  JSON->unstructured (decodeToUnstructured)"  'decodeToUnstructured'
stage "LIST decode (rest.Result / ListAndWatch list)" 'Reflector\)\.list|rest\.Result\)\.(Into|Get)'
stage "informer store/DeltaFIFO/processor"          'DeltaFIFO|threadSafeMap|processorListener|sharedIndexInformer\)\.HandleDeltas'
stage "Pod.Render (FromUnstructured+row)"           'render\.\(\*Pod\)\.Render'
stage "  FromUnstructured"                          'FromUnstructured'
stage "ui.Table.UpdateUI"                           'ui\.\(\*Table\)\.UpdateUI'
stage "tview draw"                                  'tview\.\(\*Application\)\.draw'
stage "madvise (heap growth, sysUsed)"              'runtime\.madvise'
stage "  madvise from GC/mallocgc in decode"        'runtime\.madvise' 
stage "GC bg mark"                                  'runtime\.gcBgMarkWorker'
stage "mallocgc (all)"                              'runtime\.mallocgc$'
stage "scheduler (findRunnable/wakep/kevent)"       'runtime\.(findRunnable|wakep|netpoll)$'
