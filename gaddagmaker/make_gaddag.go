// Here we have utility functions for creating a GADDAG.
package gaddagmaker

import (
	"bufio"
	"encoding/binary"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/domino14/macondo/alphabet"
)

const (
	GaddagMagicNumber = "cgdg"
	DawgMagicNumber   = "cdwg"
)

// NumArcsBitLoc is the bit location where the number of arcs start.
// A Node has a number of arcs and a letterSet
const NumArcsBitLoc = 24
const LetterSetBitMask = (1 << NumArcsBitLoc) - 1

// LetterBitLoc is the location where the letter starts.
// An Arc has a letter and a next node.
const LetterBitLoc = 24
const NodeIdxBitMask = (1 << LetterBitLoc) - 1

// Node is a temporary type used in the creation of a GADDAG.
// It will not be used when loading the GADDAG.
type Node struct {
	arcs      []*Arc
	numArcs   uint8
	letterSet alphabet.LetterSet
	// Utility fields, for minimizing GADDAG at the end:
	visited           bool
	copyOf            *Node
	depth             uint8
	letterSum         uint32
	indexInSerialized uint32
}

// Arc is also a temporary type.
type Arc struct {
	letter      rune
	destination *Node
}

// Gaddag is a temporary structure to hold the nodes in sequential order prior
// to writing them to file. It should not be used after making the gaddag.
type Gaddag struct {
	Root        *Node
	AllocStates uint32
	AllocArcs   uint32

	SerializedAlphabet   []uint32
	NumLetterSets        uint32
	SerializedLetterSets []alphabet.LetterSet
	SerializedNodes      []uint32
	Alphabet             *alphabet.Alphabet
	lexiconName          string
}

type ArcPtrSlice []*Arc

func (a ArcPtrSlice) Len() int           { return len(a) }
func (a ArcPtrSlice) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ArcPtrSlice) Less(i, j int) bool { return a[i].letter < a[j].letter }

func getWords(filename string) ([]string, *alphabet.Alphabet) {
	words := []string{}
	alphabet := alphabet.Alphabet{}
	alphabet.Init()
	file, err := os.Open(filename)
	if err != nil {
		log.Println("[WARN] Filename", filename, "not found")
		return nil, &alphabet
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		// Split line into spaces.
		fields := strings.Fields(scanner.Text())
		if len(fields) > 0 {
			word := strings.ToUpper(fields[0])
			words = append(words, word)
			err := alphabet.Update(word)
			if err != nil {
				panic(err)
			}
		}
	}
	file.Close()
	alphabet.Reconcile()
	return words, &alphabet
}

// Create a new node and store it in the node array.
func (g *Gaddag) createNode() *Node {
	newNode := Node{}
	g.AllocStates++
	return &newNode
}

// Does the node contain the letter in its letter set?
func (node *Node) containsLetter(letter rune, g *Gaddag) bool {

	val, err := g.Alphabet.Val(letter)
	if err != nil {
		panic("Unexpected error: " + err.Error())
	}
	return node.letterSet&(1<<val) != 0
}

// Does the Node contain an arc for the letter c? Return the arc if so.
func (node *Node) containsArc(c rune) *Arc {
	for i := uint8(0); i < node.numArcs; i++ {
		if node.arcs[i].letter == c {
			return node.arcs[i]
		}
	}
	return nil
}

// Creates an arc from node named "from", and returns the new node that
// this arc points to (or if to is not NULL, it returns that).
func (node *Node) createArcFrom(c rune, to *Node, g *Gaddag) *Node {
	var newNode *Node
	if to == nil {
		newNode = g.createNode()
	} else {
		newNode = to
	}
	newArc := Arc{c, nil}
	g.AllocArcs++
	node.arcs = append(node.arcs, &newArc)
	node.numArcs++
	newArc.destination = newNode
	return newNode
}

// Adds an arc from state for c (if one does not already exist) and
// resets state to the node this arc leads to. Every state has an array
// of Arc pointers. We need to create the array if it doesn't exist.
// Returns the created or existing *Node
func (node *Node) addArc(c rune, g *Gaddag) *Node {
	var nextNode *Node
	existingArc := node.containsArc(c)
	if existingArc == nil {
		nextNode = node.createArcFrom(c, nil, g)
	} else {
		nextNode = existingArc.destination
	}
	return nextNode
}

// Add arc from state to c1 and add c2 to this arc's letter set.
func (node *Node) addFinalArc(c1 rune, c2 rune, g *Gaddag) *Node {
	nextNode := node.addArc(c1, g)
	if nextNode.containsLetter(c2, g) {
		log.Fatal("[ERROR] Containsletter", nextNode, c2)
	}

	letterVal, err := g.Alphabet.Val(c2)
	if err != nil {
		panic(err)
	}
	bit := alphabet.LetterSet(1 << letterVal)
	nextNode.letterSet |= bit
	return nextNode
}

// Add an arc from state to forceState for c (an error occurs if an arc
// from st for c already exists going to any other state).
func (node *Node) forceArc(c rune, forceState *Node, g *Gaddag) {
	arc := node.containsArc(c)
	if arc != nil {
		if arc.destination != forceState {
			log.Fatal("[ERROR] Arc already existed pointing elsewhere")
		} else {
			// Don't create the arc if it already exists; redundant.
			return
		}
	}
	if node.createArcFrom(c, forceState, g) != forceState {
		log.Fatal("[ERROR] createArcFrom did not equal forceState")
	}
}

type nodeTraversalFn func(*Node)

func traverseTreeAndExecute(node *Node, fn nodeTraversalFn) {
	fn(node)
	for _, arc := range node.arcs {
		traverseTreeAndExecute(arc.destination, fn)
	}
}

func (g *Gaddag) serializeAlphabet() {
	// Append the alphabet.
	g.SerializedAlphabet = g.Alphabet.Serialize()
}

// serializeLetterSets makes a map of all unique letter sets, serializes
// to the appropriate array, and returns the map for later use.
func (g *Gaddag) serializeLetterSets() map[alphabet.LetterSet]uint32 {
	// Make a map of all unique letter sets. The value of the map is the
	// index in the serialized array.

	letterSets := make(map[alphabet.LetterSet]uint32)
	letterSetIdx := uint32(0)
	serializedLetterSets := []alphabet.LetterSet{}
	traverseTreeAndExecute(g.Root, func(node *Node) {
		node.visited = false
		if _, ok := letterSets[node.letterSet]; !ok {
			letterSets[node.letterSet] = letterSetIdx
			letterSetIdx++
			serializedLetterSets = append(serializedLetterSets, node.letterSet)
		}
	})
	log.Println("[INFO] Number of unique letter sets", len(letterSets))
	g.NumLetterSets = uint32(len(letterSets))
	g.SerializedLetterSets = serializedLetterSets
	return letterSets
}

// Serializes the elements of the gaddag into the various arrays.
func (g *Gaddag) serializeElements() {
	log.Println("[INFO] Serializing elements...")
	g.serializeAlphabet()
	letterSets := g.serializeLetterSets()
	count := uint32(0)
	g.SerializedNodes = []uint32{}
	missingElements := make(map[uint32]*Node)
	traverseTreeAndExecute(g.Root, func(node *Node) {
		if !node.visited {
			var serialized uint32
			var letterCode alphabet.MachineLetter
			var err error
			node.visited = true
			// Represent node as a 32-bit number
			serialized = letterSets[node.letterSet] +
				uint32(node.numArcs)<<NumArcsBitLoc
			g.SerializedNodes = append(g.SerializedNodes, serialized)
			node.indexInSerialized = count
			count++
			for _, arc := range node.arcs {
				if arc.letter == alphabet.SeparationToken {
					letterCode = alphabet.SeparationMachineLetter
				} else {
					letterCode, err = g.Alphabet.Val(arc.letter)
					if err != nil {
						panic(err)
					}
				}
				serialized = uint32(letterCode) << LetterBitLoc
				missingElements[count] = arc.destination
				count++
				g.SerializedNodes = append(g.SerializedNodes, serialized)
			}
		}
	})
	// Now go through the node pointers and assign SerializedElements properly.
	for idx, node := range missingElements {
		g.SerializedNodes[idx] += node.indexInSerialized
	}
	log.Println("[INFO] Assigned", len(missingElements), "missing elements.")
}

// Save saves the GADDAG or DAWG to a file.
func (g *Gaddag) Save(filename string, magicNumber string) {
	g.serializeElements()
	file, err := os.Create(filename)
	if err != nil {
		log.Fatal("[ERROR] Could not create file: ", err)
	}
	// Save it in a compressed format.
	file.WriteString(magicNumber)

	log.Printf("[INFO] Writing lexicon name: %v", g.lexiconName)
	bts := []byte(g.lexiconName)
	binary.Write(file, binary.BigEndian, uint8(len(bts)))
	binary.Write(file, binary.BigEndian, bts)
	log.Println("[INFO] Writing serialized elements")
	binary.Write(file, binary.BigEndian, g.SerializedAlphabet)
	log.Printf("[INFO] Wrote alphabet (size = %v)", g.SerializedAlphabet[0])
	binary.Write(file, binary.BigEndian, g.NumLetterSets)
	binary.Write(file, binary.BigEndian, g.SerializedLetterSets)
	log.Printf("[INFO] Wrote letter sets (num = %v)", g.NumLetterSets)
	binary.Write(file, binary.BigEndian, uint32(len(g.SerializedNodes)))
	binary.Write(file, binary.BigEndian, g.SerializedNodes)
	log.Printf("[INFO] Wrote nodes (num = %v)", len(g.SerializedNodes))
	file.Close()
	log.Println("[INFO] Saved gaddag to", filename)
}

// GenerateDawg makes a GADDAG with only one permutation of letters
// allowed per word, the spelled-out permutation. We still treat it for
// all intents and purposes as a GADDAG, but note that it only has one path!
func GenerateDawg(filename string, minimize bool, writeToFile bool) *Gaddag {
	gaddag := &Gaddag{}
	words, alphabet := getWords(filename)
	if words == nil {
		return gaddag
	}
	gaddag.lexiconName = strings.Split(filepath.Base(filename), ".")[0]
	gaddag.Root = gaddag.createNode()
	gaddag.Alphabet = alphabet
	log.Println("[INFO] Read", len(words), "words")
	for idx, word := range words {

		if idx%10000 == 0 {
			log.Printf("[DEBUG] %d...\n", idx)
		}
		st := gaddag.Root
		// Create path for a1..an-1:
		wordRunes := []rune(word)
		n := len(wordRunes)
		for j := 0; j < n-2; j++ {
			st = st.addArc(wordRunes[j], gaddag)
		}

		st = st.addFinalArc(wordRunes[n-2], wordRunes[n-1], gaddag)
	}
	log.Printf("[INFO] Allocated arcs: %d states: %d\n", gaddag.AllocArcs,
		gaddag.AllocStates)
	// We need to also sort the arcs alphabetically prior to minimization/
	// serialization.
	traverseTreeAndExecute(gaddag.Root, func(node *Node) {
		sort.Sort(ArcPtrSlice(node.arcs))
	})
	if minimize {
		gaddag.Minimize()
	} else {
		log.Println("[INFO] Not minimizing.")
	}
	if writeToFile {
		gaddag.Save("out.dawg", DawgMagicNumber)
	}
	return gaddag
}

// GenerateGaddag makes a GADDAG out of the filename, and optionally
// minimizes it and/or writes it to file.
func GenerateGaddag(filename string, minimize bool, writeToFile bool) *Gaddag {
	gaddag := &Gaddag{}
	words, alph := getWords(filename)
	if words == nil {
		return gaddag
	}
	gaddag.lexiconName = strings.Split(filepath.Base(filename), ".")[0]
	gaddag.Root = gaddag.createNode()
	gaddag.Alphabet = alph
	log.Println("[INFO] Read", len(words), "words")
	for idx, word := range words {
		if idx%10000 == 0 {
			log.Printf("[DEBUG] %d...\n", idx)
		}
		st := gaddag.Root
		// Create path for anan-1...a1:
		wordRunes := []rune(word)
		n := len(wordRunes)
		for j := n - 1; j >= 2; j-- {
			st = st.addArc(wordRunes[j], gaddag)
		}
		st = st.addFinalArc(wordRunes[1], wordRunes[0], gaddag)

		// Create path for an-1...a1^an
		st = gaddag.Root
		for j := n - 2; j >= 0; j-- {
			st = st.addArc(wordRunes[j], gaddag)
		}
		st = st.addFinalArc(alphabet.SeparationToken, wordRunes[n-1], gaddag)

		// Partially minimize remaining paths.
		for m := n - 3; m >= 0; m-- {
			forceSt := st
			st = gaddag.Root
			for j := m; j >= 0; j-- {
				st = st.addArc(wordRunes[j], gaddag)
			}
			st = st.addArc(alphabet.SeparationToken, gaddag)
			st.forceArc(wordRunes[m+1], forceSt, gaddag)
		}
	}
	log.Printf("[INFO] Allocated arcs: %d states: %d\n", gaddag.AllocArcs,
		gaddag.AllocStates)
	// We need to also sort the arcs alphabetically prior to minimization/
	// serialization.
	traverseTreeAndExecute(gaddag.Root, func(node *Node) {
		sort.Sort(ArcPtrSlice(node.arcs))
	})
	if minimize {
		gaddag.Minimize()
	} else {
		log.Println("[INFO] Not minimizing.")
	}
	if writeToFile {
		gaddag.Save("out.gaddag", GaddagMagicNumber)
	}
	return gaddag
}