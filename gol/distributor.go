package gol

import (
	"fmt"
	"strconv"
	"time"

	"uk.ac.bris.cs/gameoflife/util"
)

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan string
	Input      chan uint8
	Output     chan uint8
	keyPresses <-chan rune
}

const alive = 255
const dead = 0

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels) {
	//initialize values and channels
	ticker := time.NewTicker(2 * time.Second)
	done := make(chan bool)
	turns := 0
	world := makeWorld(p.ImageHeight, p.ImageWidth)
	pauseChan := make(chan bool)
	exitChan := make(chan bool)
	pause := false

	// Get filename and call input command to initialize world and call Cellflipped Event on alive cells
	c.ioCommand <- ioInput
	filename := strconv.Itoa(p.ImageWidth) + "x" + strconv.Itoa(p.ImageHeight)
	c.ioFilename <- filename
	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			world[y][x] = <-c.Input
			if world[y][x] == alive {
				eventCellFlipped := CellFlipped{CompletedTurns: 0, Cell: util.Cell{X: x, Y: y}}
				c.events <- eventCellFlipped
			}
		}
	}

	//initialize alive cells from the new world
	aliveCells := calculateAliveCells(p, world)

	// set up ticker to tick every 2 second and gets the alive cell count
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if turns == 0 || pause {
					break
				}
				aliveCells := len((calculateAliveCells(p, world)))
				eventAliveCellsCount := AliveCellsCount{CompletedTurns: turns, CellsCount: aliveCells}
				c.events <- eventAliveCellsCount
			}
		}
	}()

	// This go routine manages all key pressing operations
	// changes pause to its opposite state and send down channel whenever 'p' is pressed so it can determine when to stop or start executing again
	// 's' and 'q' can still be pressed when paused
	go func() {
		for {
			select {
			case key := <-c.keyPresses:
				switch key {
				case 's':
					generateOutputFile(c, filename, turns, p, world)
				case 'q':
					exitChan <- true
				case 'p':
					if pause {
						pause = false
						fmt.Println("Continuing")
						pauseChan <- pause
					} else {
						pause = true
						c.events <- StateChange{turns, Executing}
						pauseChan <- pause
						c.events <- StateChange{turns, Paused}
					}
				}
			}
		}
	}()

	//Calculates the length for each worker and intialize worker channel
	dividedLength := p.ImageHeight / p.Threads
	checkRemainder := p.ImageHeight % p.Threads
	workerChannels := make([]chan [][]byte, p.Threads)
	for i := range workerChannels {
		workerChannels[i] = make(chan [][]byte)
	}

	//generate a closure of the current world
	immutableWorld := makeImmutableWorld(world)

	//execute all turns and sends out workers
	//select statements are used in order to manage channels from key press so the program knows when to pause or exit the program
	exit := false
	for turns = 0; turns < p.Turns; turns++ {
		select {
		case <-pauseChan:
			select {
			case <-pauseChan:
				break
			case exit = <-exitChan:
				break
			}
		case exit = <-exitChan:
		default:
			for i := 0; i < p.Threads; i++ {
				//if the length cannot be evenly divided, allow the last worker to do all the remaining extra length of the image
				if checkRemainder != 0 && i == p.Threads-1 {
					go worker(i*dividedLength, p.ImageHeight, 0, p.ImageWidth, p, immutableWorld, c.events, turns, workerChannels[i])
				} else {
					go worker(i*dividedLength, (i+1)*dividedLength, 0, p.ImageWidth, p, immutableWorld, c.events, turns, workerChannels[i])
				}
			}
			//collect workers and combine results
			newWorld := makeWorld(0, 0)
			for i := 0; i < p.Threads; i++ {
				part := <-workerChannels[i]
				newWorld = append(newWorld, part...)
			}
			world = newWorld

			//get slice of alive cells from updated world
			aliveCells = calculateAliveCells(p, world)

			//generate new closure of the updated world
			immutableWorld = makeImmutableWorld(world)

			//call turn complete event when all process for one turn finish
			eventTurnComplete := TurnComplete{CompletedTurns: turns}
			c.events <- eventTurnComplete
		}
		//if exit is true (only changes when 'q' is pressed), exit the execution process
		if exit {
			break
		}
	}

	//stop ticker
	ticker.Stop()
	done <- true

	//call final turn complete event
	eventFinalTurnComplete := FinalTurnComplete{CompletedTurns: turns, Alive: aliveCells}
	c.events <- eventFinalTurnComplete

	//call output command and writes a new file of the world
	generateOutputFile(c, filename, turns, p, world)

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	//This was originally: c.events <- StateChange{p.Turns, Quitting}
	c.events <- StateChange{turns, Quitting}
	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}

//call output command and writes a new file of the world
func generateOutputFile(c distributorChannels, filename string, turns int, p Params, world [][]byte) {
	c.ioCommand <- ioOutput
	filename = filename + "x" + strconv.Itoa(turns)
	c.ioFilename <- filename
	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			c.Output <- world[y][x]
		}
	}
	//call image output complete event
	eventImageOutputComplete := ImageOutputComplete{CompletedTurns: turns, Filename: filename}
	c.events <- eventImageOutputComplete
}

func mod(x, m int) int {
	return (x + m) % m
}

//make closure
func makeImmutableWorld(world [][]byte) func(y, x int) byte {
	return func(y, x int) byte {
		return world[y][x]
	}
}

//make empty new world with given dimension
func makeWorld(height, width int) [][]byte {
	world := make([][]byte, height)
	for i := range world {
		world[i] = make([]byte, width)
	}
	return world
}

//worker function
func worker(startY, endY, startX, endX int, p Params, world func(y, x int) byte, events chan<- Event, turns int, out chan<- [][]byte) {
	newWorld := calculateNextState(startY, endY, startX, endX, p, world, events, turns)
	out <- newWorld
}

//calculates surrounding neighbours
func calculateNeighbours(p Params, x, y int, world func(y, x int) byte) int {
	neighbours := 0
	for i := -1; i <= 1; i++ {
		for j := -1; j <= 1; j++ {
			if i != 0 || j != 0 {
				if world(mod(y+i, p.ImageHeight), mod(x+j, p.ImageWidth)) == alive {
					neighbours++
				}
			}
		}
	}
	return neighbours
}

//calculates next state for the world with the provided dimension, varys from workers
func calculateNextState(startY, endY, startX, endX int, p Params, world func(y, x int) byte, events chan<- Event, turns int) [][]byte {
	height := endY - startY
	width := endX - startX
	newWorld := makeWorld(height, width)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			neighbours := calculateNeighbours(p, startX+x, startY+y, world)
			//call CellFlipped event when a cell state is changed
			if world(y+startY, x+startX) == alive {
				if neighbours == 2 || neighbours == 3 {
					newWorld[y][x] = alive
				} else {
					newWorld[y][x] = dead
					eventCellFlipped := CellFlipped{CompletedTurns: turns, Cell: util.Cell{X: startX + x, Y: startY + y}}
					events <- eventCellFlipped
				}
			} else {
				if neighbours == 3 {
					newWorld[y][x] = alive
					eventCellFlipped := CellFlipped{CompletedTurns: turns, Cell: util.Cell{X: startX + x, Y: startY + y}}
					events <- eventCellFlipped
				} else {
					newWorld[y][x] = dead
				}
			}
		}
	}
	return newWorld
}

//calculate all alive cells and store into a slice
func calculateAliveCells(p Params, world [][]byte) []util.Cell {
	aliveCells := []util.Cell{}
	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			if world[y][x] == alive {
				aliveCells = append(aliveCells, util.Cell{X: x, Y: y})
			}
		}
	}
	return aliveCells
}
